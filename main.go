package main

import (
	"bytes"
	"code.google.com/p/go.net/html"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"github.com/peterbourgon/diskv"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var (
	apiKey     = flag.String("a", "", "Readibility API Key")
	sr         = flag.String("r", "golang", "comma separated list of subreddits to create rss feeds for.")
	update     = flag.Int("u", 30, "update interval (in minutes)")
	listen     = flag.String("l", ":8080", "Address to listen on")
	cacheFile  = flag.String("c", fmt.Sprintf("%s/cache.diskv", os.TempDir()), "Cache file")
	subreddits []string
	rssDir     = fmt.Sprintf("%s/%s", os.TempDir(), "dereddit")
	cache      *diskv.Diskv
)

const (
	Version     = "0.2.0"
	readability = "http://www.readability.com/api/content/v1/parser"
)

type rss struct {
	Channels []Channel `xml:"channel"`
	Version  string    `xml:"version,attr"`
}

type Channel struct {
	Docs          string
	Title         string `xml:"title"`
	Link          string `xml:"link"`
	Description   string `xml:"description"`
	Language      string `xml:"language"`
	WebMaster     string `xml:"webMaster,omitempty"`
	Generator     string `xml:"generator"`
	PubDate       string `xml:"pubDate"`
	LastBuildDate string `xml:"lastBuildDate"`
	Items         []Item `xml:"item"`
}

type Item struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	Author      string `xml:"author,omitempty"`
	Category    string `xml:"category,omitempty"`
	Comments    string `xml:"comments,omitempty"`
	GUID        string `xml:"guid",omitempty`
	//PubDate     time.Time `xml:"pubDate"`
}

type ReadabilityResp struct {
	Author     string
	Content    string
	Domain     string
	Title      string
	Excerpt    string
	Direction  string
	WordCount  int `json:"word_count"`
	TotalPages int `json:"total_pages"`
	NextPageId int `json:"next_page_id,omitempty"`
}

func mkItem(desc string) (Item, error) {
	var nodes []*html.Node
	var link, title, content string
	var find func(*html.Node)
	doc, err := html.Parse(strings.NewReader(desc))
	if err != nil {
		return Item{}, err
	}
	find = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			if n.FirstChild.Data == "[link]" {
				nodes = append(nodes, n)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	for _, v := range nodes {
		var err error
		for _, a := range v.Attr {
			if a.Key == "href" {
				link = a.Val
				break
			}
		}
		title, content, err = readable(link)
		if err != nil {
			log.Printf("%+v\n", err)
			continue
		}
	}
	return Item{Title: title, Link: link, Description: content, GUID: link}, nil
}

func readable(article string) (string, string, error) {
	var r ReadabilityResp
	h := fnv.New64a()
	io.WriteString(h, article)
	key := fmt.Sprintf("%x", h.Sum(nil))
	if cache.Has(key) {
		log.Printf("cache hit for %s\n", article)
		var c ReadabilityResp
		b, err := cache.Read(key)
		if err != nil {
			return "", "", err
		}
		d := json.NewDecoder(bytes.NewReader(b))
		err = d.Decode(&c)
		if err != nil {
			return "", "", err
		}
		return c.Title, html.EscapeString(c.Content), nil
	}
	log.Printf("fetching '%s'\n", article)
	v := url.Values{}
	v.Add("token", *apiKey)
	v.Add("url", article)
	res, err := http.Get(fmt.Sprintf("%s?%s", readability, v.Encode()))
	if err != nil {
		return "", "", err
	}
	d := json.NewDecoder(res.Body)
	d.Decode(&r)
	defer res.Body.Close()
	title := r.Title
	b, err := json.Marshal(r)
	if err != nil {
		return "", "", err
	}
	err = cache.Write(key, b)
	if err != nil {
		log.Println(err)
	}
	return title, html.EscapeString(r.Content), nil
}

func init() {
	flag.Parse()
	subreddits = strings.Split(*sr, ",")
	log.Printf("watching subreddits: %v\n", subreddits)
	os.Mkdir(rssDir, 0777)
	if *apiKey == "" {
		log.Fatalln("api key not specified")
	}
	if *cacheFile == "" {
		log.Fatalln("cache file is empty")
	}
	o := diskv.Options{
		BasePath:    *cacheFile,
		Compression: diskv.NewGzipCompression(),
		PathPerm:    0755,
		FilePerm:    0666,
	}
	cache = diskv.New(o)
	log.Printf("cache %s opened\n", *cacheFile)
}

func main() {
	var manual chan time.Time = make(chan time.Time)
	ticker := time.NewTicker(time.Duration(*update) * time.Minute)
	for _, reddit := range subreddits {
		log.Printf("Launching goroutine for %s\n", reddit)
		go func(reddit string, update <-chan time.Time, manual <-chan time.Time) {
			var u time.Time
			for {
				select {
				case u := <-update:
					log.Printf("recvd tick (%v) to update /r/%s\n", u, reddit)
				case <-manual:
					log.Printf("recvd manual tick (%v) to update /r/%s\n", u, reddit)
				}
				var subreddit rss
				var items []Item
				r, err := http.Get(fmt.Sprintf("http://www.reddit.com/r/%s/.rss", reddit))
				if err != nil {
					log.Fatal(err)
				}
				d := xml.NewDecoder(r.Body)
				d.Strict = false
				defer r.Body.Close()
				err = d.Decode(&subreddit)
				if err != nil {
					log.Fatal(err)
				}
				for _, i := range subreddit.Channels[0].Items {
					ni, err := mkItem(i.Description)
					if err != nil {
						log.Println(err)
						continue
					}
					items = append(items, ni)
				}
				feed, err := os.Create(fmt.Sprintf("%s/%s.xml", rssDir, reddit))
				if err != nil {
					log.Fatal(err)
				}
				defer feed.Close()
				io.WriteString(feed, xml.Header)
				e := xml.NewEncoder(feed)
				e.Indent("", "\t")
				now := time.Now().UTC().Format(time.RFC822)
				var f rss = rss{
					Version: "2.0",
					Channels: []Channel{Channel{
						Title:         reddit,
						Docs:          "http://blogs.law.harvard.edu/tech/rss",
						Language:      "en-us",
						PubDate:       now,
						LastBuildDate: now,
						Description:   fmt.Sprintf("Articles pulled from /r/%s", reddit),
						Link:          fmt.Sprintf("http://www.reddit.com/r/%s", reddit),
						Generator:     fmt.Sprintf("dereddit v%s", Version),
						Items:         items},
					},
				}
				err = e.Encode(f)
				if err != nil {
					log.Println(err)
				}
			}
		}(reddit, ticker.C, manual)
	}
	manual <- time.Now().UTC()
	log.Println("Starting HTTP server")
	log.Fatal(http.ListenAndServe(*listen, http.FileServer(http.Dir(rssDir))))
}
