package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

func fetchDumbDomains(url string) ([]string, error) {
	req, err := http.NewRequest("GET", url, bytes.NewBuffer([]byte{}))
	if err != nil {
		return []string{}, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return []string{}, err
	}

	doms := []string{}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return []string{}, err
	}

	err = json.Unmarshal(data, &doms)
	return doms, err
}

type DpiDomainRecord struct {
	Domains []string `json:"domains"`
}

func fetchDpiDomains() ([]string, error) {
	req, err := http.NewRequest("GET", "https://reestr.rublacklist.net/api/v3/dpi/", bytes.NewBuffer([]byte{}))
	if err != nil {
		return []string{}, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return []string{}, err
	}

	doms := []DpiDomainRecord{}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return []string{}, err
	}

	err = json.Unmarshal(data, &doms)
	if err != nil {
		return []string{}, err
	}
	ds := []string{}

	for _, d := range doms {
		ds = append(ds, d.Domains...)
	}

	return ds, nil
}

var dpiF = flag.Bool("dpi", true, "Fetch DPI domains")
var ctF = flag.Bool("ct", true, "Fetch CT domains")
var domainsF = flag.Bool("domains", true, "Fetch domains from general domain list")
var out = flag.String("out", "", "File to save domains")

func main() {
	flag.Parse()
	begin := time.Now()
	domains := []string{}

	if *ctF {
		ct, err := fetchDumbDomains("https://reestr.rublacklist.net/api/v3/ct-domains/")
		if err == nil {
			slog.Info("Fetched domains", "list", "ct", "total", len(ct))
			domains = append(domains, ct...)
		} else {
			slog.Error("Failed to fetch domains", "list", "ct", "error", err)
		}
	}

	if *domainsF {
		doms, err := fetchDumbDomains("https://reestr.rublacklist.net/api/v3/domains/")
		if err == nil {
			slog.Info("Fetched domains", "list", "domains", "total", len(doms))
				domains = append(domains, doms...)

		} else {
			slog.Error("Failed to fetch domains", "list", "domains", "error", err)
		}
	}

	if *dpiF {
		dpi, err := fetchDpiDomains()
		if err == nil {
			slog.Info("Fetched domains", "list", "dpi", "total", len(dpi))
			domains = append(domains, dpi...)
		} else {
			slog.Error("Failed to fetch domains", "list", "dpi", "error", err)
		}
	}

	if len(*out) > 0 {
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			slog.Error("Failed to open file", "file", out, "error", err)
			return
		}
		for _, d := range domains {
			if strings.Contains(d, "\"") {
				d = strings.ReplaceAll(d, "\"", "")
			}
			_, err := fmt.Fprintln(f, d)
			if err != nil {
				slog.Error("Failed to write to file", "file", *out, "error", err, "domain", d)
			}
		}
		slog.Info("Saved domains to a file", "file", *out)
	}

	slog.Info("Done", "time", time.Since(begin))
}