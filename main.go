// +build !windows

package main

import (
	"bytes"
	"code.google.com/p/go.net/html"
	"encoding/gob"
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
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var (
	apiKey     = flag.String("a", "", "Readibility API Key")
	sr         = flag.String("r", "golang", "Comma separated list of subreddits to create rss feeds for.")
	update     = flag.Int("u", 30, "Update interval (in minutes)")
	noListen   = flag.Bool("n", false, "don't start the internal HTTP server")
	listen     = flag.String("l", ":8080", "Address to listen on")
	ul         = flag.String("U", "", "Comma separated list of users to ignore.")
	selfOK     = flag.Bool("s", false, "Allow self posts into generated feed.")
	purgeTime  = flag.Int("P", 7, "Time to purge articles after, in days.")
	rssDir     = flag.String("d", fmt.Sprintf("%s/dereddit", os.TempDir()), "Directory to output rss feeds to.")
	verbose    = flag.Bool("v", false, "Print additional information")
	confidence = flag.Float64("c", 0.5, "Confidence threshold. Articles with parse confidence below this are not included.")

	cache         *diskv.Diskv
	subreddits    []string
	userBlacklist []string
	noUpdate      = false
)

const (
	// Version (in case we want to print it out later)
	Version     = "0.9.0"
	readability = "http://www.readability.com/api/content/v1/"
)

type rss struct {
	Channels []Channel `xml:"channel"`
	Version  string    `xml:"version,attr"`
}

// Channel is an RSS Channel
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

// Item is an RSS Item
type Item struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	Author      string `xml:"author,omitempty"`
	Category    string `xml:"category,omitempty"`
	Comments    string `xml:"comments,omitempty"`
	GUID        string `xml:"guid,omitempty"`
	//PubDate     time.Time `xml:"pubDate"`
}

// ReadabilityResp is the response we get back
type ReadabilityResp struct {
	Author     string
	Content    string
	Domain     string
	Title      string
	Excerpt    string
	Direction  string
	WordCount  int       `json:"word_count"`
	TotalPages int       `json:"total_pages"`
	NextPageID int       `json:"next_page_id,omitempty"`
	Date       time.Time `json:",omitempty"`
}

type redditStub struct {
	Link     string
	User     string
	Comments string
}

func parseStub(stub string) (r redditStub, err error) {
	var extract func(*html.Node)
	var doc *html.Node
	doc, err = html.Parse(strings.NewReader(stub))
	if err != nil {
		return
	}
	extract = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			switch {
			case n.FirstChild.Data == "[link]":
				r.Link = n.Attr[0].Val
			case strings.HasSuffix(n.FirstChild.Data, " comments]"):
				r.Comments = n.Attr[0].Val
			case strings.HasPrefix(n.Attr[0].Val, "http://www.reddit.com/user/"):
				r.User = strings.TrimSpace(n.FirstChild.Data)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extract(c)
		}
	}
	extract(doc)
	return
}

func mkItem(desc string) (*Item, error) {
	var i Item
	var err error
	var r ReadabilityResp
	var resp *http.Response
	var s redditStub
	s, err = parseStub(desc)
	if err != nil {
		return nil, err
	}
	if s.Link == s.Comments && !*selfOK {
		if *verbose {
			log.Printf("Ignoring: %s (self post)\n", s.Link)
		}
		return nil, nil
	}
	for _, u := range userBlacklist {
		if u == s.User {
			if *verbose {
				log.Printf("Ignoring: %s (bad user: %s)\n", s.Link, u)
			}
			return nil, nil
		}
	}
	// if we can't get the confidence for some reason, just keep chugging.
	if c, _ := checkConfidence(s.Link); c < *confidence && err == nil {
		if *verbose {
			log.Printf("Ignoring: %s (below confidence)\n", s.Link)
		}
		return nil, nil
	}
	resp, err = http.Head(s.Link)
	if err != nil {
		return nil, err
	}
	i.Link = s.Link
	i.GUID = s.Link
	i.Comments = s.Comments
	switch {
	case strings.HasPrefix(resp.Header.Get("Content-Type"), "image/"):
		if *verbose {
			log.Printf("Found image: %s\n", s.Link)
		}
		i.Title = "Image"
		i.Description = fmt.Sprintf("<img src=\"%s\" alt=\"Image\" />", s.Link)
	default:
		r, err = readable(s.Link)
		if err != nil {
			return nil, err
		}
		i.Title = r.Title
		i.Description = r.Content
		if r.Author != "" {
			i.Author = r.Author
		} else {
			i.Author = fmt.Sprintf("submitted by %s", s.User)
		}
	}
	return &i, nil
}

func urlToKey(url string) (key string) {
	h := fnv.New64a()
	io.WriteString(h, url)
	key = fmt.Sprintf("%x", h.Sum(nil))
	return
}

func loadCache(key string) (r ReadabilityResp) {
	b, err := cache.Read(key)
	if err != nil {
		return
	}
	d := gob.NewDecoder(bytes.NewReader(b))
	err = d.Decode(&r)
	if err != nil {
		return
	}
	return
}

func readable(article string) (ReadabilityResp, error) {
	var r ReadabilityResp
	key := urlToKey(article)
	if cache.Has(key) {
		if *verbose {
			log.Printf("Cache hit: %s\n", article)
		}
		return loadCache(key), nil
	}

	if *verbose {
		log.Printf("Fetching: %s\n", article)
	}
	v := url.Values{}
	v.Add("token", *apiKey)
	v.Add("url", article)
	res, err := http.Get(fmt.Sprintf("%s?%s", fmt.Sprintf("%s/parser", readability), v.Encode()))
	if err != nil {
		return r, err
	}
	d := json.NewDecoder(res.Body)
	d.Decode(&r)
	defer res.Body.Close()

	r.Date = time.Now().UTC()

	b := bytes.Buffer{}
	enc := gob.NewEncoder(b)
	err = enc.Encode(r)
	if err != nil {
		return r, err
	}
	err = cache.WriteStream(key, b, false)
	if err != nil {
		log.Println(err)
	}
	return r, nil
}

func checkConfidence(u string) (float64, error) {
	var r struct {
		URL        string `json:"url"`
		Confidence float64
	}
	v := url.Values{}
	v.Add("url", u)
	res, err := http.Get(fmt.Sprintf("%s?%s", fmt.Sprintf("%s/confidence", readability), v.Encode()))
	if err != nil {
		return 0.0, err
	}
	d := json.NewDecoder(res.Body)
	defer res.Body.Close()
	d.Decode(&r)
	return r.Confidence, nil
}

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Updates can be triggered by sending SIGUSR1.")
		fmt.Fprintln(os.Stderr, "Automatic updates can be toggled by sending SIGUSR2.")
		fmt.Fprintln(os.Stderr, "A cache purge can be triggered by sending SIGHUP.")
		fmt.Fprintln(os.Stderr)
	}
	flag.Parse()
	subreddits = strings.Split(*sr, ",")
	log.Printf("watching subreddits: %v\n", subreddits)
	userBlacklist = strings.Split(*ul, ",")
	log.Printf("ignoring users: %v\n", userBlacklist)
	cacheDir := fmt.Sprintf("%s/dereddit.cache", os.TempDir())
	os.Mkdir(*rssDir, 0777)
	if *apiKey == "" {
		log.Fatalln("api key not specified")
	}
	o := diskv.Options{
		BasePath: cacheDir,
		//Compression: diskv.NewGzipCompression(),
		PathPerm: 0755,
		FilePerm: 0666,
	}
	cache = diskv.New(o)
	log.Printf("confidence set to %f\n", *confidence)
	log.Printf("cache dir '%s' opened\n", cacheDir)
	log.Printf("outputting rss feeds to '%s'\n", *rssDir)
}

func main() {
	var manual []chan time.Time
	sigusr1 := make(chan os.Signal, 1)
	signal.Notify(sigusr1, syscall.SIGUSR1)

	toggleUpdate := make(chan os.Signal, 1)
	signal.Notify(toggleUpdate, syscall.SIGUSR2)

	cleanCache := make(chan os.Signal, 1)
	signal.Notify(cleanCache, syscall.SIGHUP)

	for i, reddit := range subreddits {
		ticker := time.NewTicker(time.Duration(*update) * time.Minute)
		if *verbose {
			log.Printf("Launching goroutine for %s\n", reddit)
		}
		manual = append(manual, make(chan time.Time))
		go func(reddit string, update <-chan time.Time, manual <-chan time.Time) {
			for {
				select {
				case <-update:
					if noUpdate {
						log.Printf("ignoring tick to update /r/%s\n", reddit)
						continue
					} else {
						log.Printf("received tick to update /r/%s\n", reddit)
					}
				case <-manual:
					log.Printf("received signal to update /r/%s\n", reddit)
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
					if ni == nil {
						continue
					}
					items = append(items, *ni)
				}
				feed, err := os.Create(fmt.Sprintf("%s/%s.xml", *rssDir, reddit))
				if err != nil {
					log.Fatal(err)
				}
				io.WriteString(feed, xml.Header)
				e := xml.NewEncoder(feed)
				e.Indent("", "\t")
				now := time.Now().UTC().Format(time.RFC822)
				f := rss{
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
				feed.Close()
			}
		}(reddit, ticker.C, manual[i])
	}

	go func(c <-chan os.Signal) {
		tick := time.Tick(time.Duration(12) * time.Hour)
		for {
			select {
			case <-c:
			case <-tick:
			}
			log.Println("Cache clean triggered.")
			for c := range cache.Keys() {
				a := loadCache(c)
				if time.Since(a.Date) > (time.Duration(*purgeTime) * (time.Duration(24) * time.Hour)) {
					if *verbose {
						log.Printf("Expiring cache: %s\n", c)
					}
					cache.Erase(c)
				}
			}
		}
	}(cleanCache)

	go func(c <-chan os.Signal) {
		for _ = range c {
			noUpdate = !noUpdate
			if noUpdate {
				log.Println("automatic updates: off")
			} else {
				log.Println("automatic updates: on")
			}
		}
	}(toggleUpdate)

	go func(c <-chan os.Signal) {
		for _ = range c {
			for i := range subreddits {
				manual[i] <- time.Now().UTC()
			}
		}
	}(sigusr1)

	cleanCache <- syscall.SIGHUP
	sigusr1 <- syscall.SIGUSR1

	if !*noListen {
		log.Println("Starting HTTP server")
		log.Fatal(http.ListenAndServe(*listen, http.FileServer(http.Dir(*rssDir))))
	} else {
		var ch chan bool
		<-ch
	}
}
