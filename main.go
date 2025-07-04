package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var addr string
var in string
var port int
var thread int
var out string
var timeout int
var verbose bool
var enableIPv6 bool
var url string
var best int
var maxWait int
var bestOut string
var ignoreDomains string

var cache map[string]bool = nil

var builtinIgnoreList = []string{
	"cloudflare",
	"vpn",
	"shuoki",
	"ayugram",
	"akisearch",
	"digitalocean",
	"hetzner",
	"hostopia",
	"aeza",
}

func isIgnored(s string) bool {
	for _, c := range builtinIgnoreList {
		if strings.Contains(s, c) {
			return false
		}
	}

	if cache != nil {
		return cache[s]
	}
	if len(ignoreDomains) == 0 {
		return false
	}
	data, err := os.ReadFile(ignoreDomains)
	if err != nil {
		return false
	}
	cache := map[string]bool{}
	for _, d := range strings.Split(string(data), "\n") {
		cache[d] = true
	}
	return cache[s]
}

func main() {
	_ = os.Unsetenv("ALL_PROXY")
	_ = os.Unsetenv("HTTP_PROXY")
	_ = os.Unsetenv("HTTPS_PROXY")
	_ = os.Unsetenv("NO_PROXY")
	flag.StringVar(&addr, "addr", "", "Specify an IP, IP CIDR or domain to scan")
	flag.StringVar(&in, "in", "", "Specify a file that contains multiple "+
		"IPs, IP CIDRs or domains to scan, divided by line break")
	flag.IntVar(&port, "port", 443, "Specify a HTTPS port to check")
	flag.IntVar(&thread, "thread", 2, "Count of concurrent tasks")
	flag.StringVar(&out, "out", "out.csv", "Output file to store the result")
	flag.IntVar(&timeout, "timeout", 10, "Timeout for every check")
	flag.BoolVar(&verbose, "v", false, "Verbose output")
	flag.BoolVar(&enableIPv6, "46", false, "Enable IPv6 in additional to IPv4")
	flag.StringVar(&url, "url", "", "Crawl the domain list from a URL, "+
		"e.g. https://launchpad.net/ubuntu/+archivemirrors")
	flag.IntVar(&best, "best", 0, "Pick the best server out of N specified")
	flag.IntVar(&maxWait, "wait", 15, "Maximum wait time in seconds")
	flag.StringVar(&bestOut, "bestOut", "", "Best server output")
	flag.StringVar(&ignoreDomains, "ignoreDomains", "", "Path to a file containing domains to be ignored")
	flag.Parse()
	if verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})))
	}
	if !ExistOnlyOne([]string{addr, in, url}) {
		slog.Error("You must specify and only specify one of `addr`, `in`, or `url`")
		flag.PrintDefaults()
		return
	}
	outWriter := io.Discard
	if out != "" {
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			slog.Error("Error opening file", "path", out)
			return
		}
		defer f.Close()
		_, _ = f.WriteString("IP,ORIGIN,CERT_DOMAIN,CERT_ISSUER,GEO_CODE\n")
		outWriter = f
	}
	var hostChan <-chan Host
	if addr != "" {
		hostChan = IterateAddr(addr)
	} else if in != "" {
		f, err := os.Open(in)
		if err != nil {
			slog.Error("Error reading file", "path", in)
			return
		}
		defer f.Close()
		hostChan = Iterate(f)
	} else {
		slog.Info("Fetching url...")
		resp, err := http.Get(url)
		if err != nil {
			slog.Error("Error fetching url", "err", err)
			return
		}
		defer resp.Body.Close()
		v, err := io.ReadAll(resp.Body)
		if err != nil {
			slog.Error("Error reading body", "err", err)
			return
		}
		arr := regexp.MustCompile("(http|https)://(.*?)[/\"<>\\s]+").FindAllStringSubmatch(string(v), -1)
		var domains []string
		for _, m := range arr {
			domains = append(domains, m[2])
		}
		domains = RemoveDuplicateStr(domains)
		slog.Info("Parsed domains", "count", len(domains))
		hostChan = Iterate(strings.NewReader(strings.Join(domains, "\n")))
	}

	chs := []*ScanResponse{}
	var mux sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())

	outCh := OutWriter(outWriter)
	defer close(outCh)
	geo := NewGeo()
	lastChecked := time.Now()
	var wg sync.WaitGroup
	wg.Add(thread)
	for i := 0; i < thread; i++ {
		go func() {
			for {
				if time.Since(lastChecked) > time.Duration(maxWait) * time.Second {
					wg.Done()
					cancel()
					return
				}
				select {
				case <-ctx.Done():
					wg.Done()
					return
				case ip, ok := <-hostChan:
					if !ok {
						wg.Done()
						return
					}
					h := ScanTLS(ip, outCh, geo)
					if h != nil && (len(h.Domain) == 0 || isIgnored(h.Domain)) {
						slog.Info("Ignoring domain", "domain", h.Domain)
					}
					if h == nil {
						continue
					}
					mux.Lock()
					chs = append(chs, h)
					lastChecked = time.Now()
					mux.Unlock()
					if len(chs) >= best && best > 0 {
						wg.Done()
						cancel()
						return
					}
				}
			}
		}()
	}

	t := time.Now()
	slog.Info("Started all scanning threads", "time", t)
	wg.Wait()

	if best != 0 {
		bestPing := -1
		bestServer := &ScanResponse{}
		for _, ch := range chs {
			ping, err := CheckPing(ch)
			if err != nil {
				slog.Error("Failed to check ping", "server", ch.Domain, "error", err)
			} else {
				slog.Info("Checked ping", "server", ch.Domain, "ping", ping)
				if ping <= int64(bestPing) || bestPing == -1 {
					bestServer = ch
					bestPing = int(ping)
				}
			}
		}

		if bestPing == -1 {
			bestServer.Domain = "yahoo.com"
		}

		if best != 0 {
			slog.Info("Best server found", "server", bestServer.Domain, "ping", bestPing)
			if len(bestOut) > 0 {
				err := os.WriteFile(bestOut, []byte(bestServer.Domain), 0777)
				if err != nil {
					slog.Error("Failed to save best server", "path", bestOut, "error", err)
				}
			}
		}
	}

	slog.Info("Scanning completed", "time", time.Now(), "elapsed", time.Since(t).String())
}
