dereddit
========

Want to use the hivemind to dump stories directly into your feedreader? Now you
can.

`dereddit` takes an rss feed from reddit and strips out the reddit parts. It
uses [Readability](http://www.readability.com) to fetch the content from the
resulting links and serve it back to you.

building
--------

	% go get github.com/hdonnay/dereddit
	% go install github.com/hdonnay/dereddit

or

	% git clone https://github.com/hdonnay/dereddit
	% cd dereddit
    % go run make.go

help
----

Presently, there aren't many knobs to turn:

    % ./dereddit -h

usage
-----

You will need a [Readability API](http://www.readability.com/developers/api) key
for this. It it used to parse the links and return the useful bits of the
webpage.

    % ./dereddit -a "00...00" -u 60 -r TrueReddit,golang,indepthstories

The above line would create rss feeds for /r/TrueReddit, /r/golang, and
/r/indepthstories and update them every 60 minutes. They would be accessable at
`http://localhost:8080/<subreddit>.xml` (http://localhost:8080/golang.xml for
example).
