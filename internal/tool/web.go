package tool

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"winky/internal/llm"
)

const (
	maxFetchBytes   = 1 << 20 // 1 MiB read per page
	searchUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36"
)

var (
	ddgLink    = regexp.MustCompile(`(?s)<a[^>]*class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippet = regexp.MustCompile(`(?s)<a[^>]*class="result__snippet"[^>]*>(.*?)</a>`)
	tagRe      = regexp.MustCompile(`<[^>]+>`)
)

var webSearchSpec = llm.ToolSpec{
	Name:        "web_search",
	Description: "Search the web and return the top results (title, URL, snippet).",
	Parameters: object(
		map[string]any{
			"query": stringProp("Search query."),
		},
		"query",
	),
}

var webFetchSpec = llm.ToolSpec{
	Name:        "web_fetch",
	Description: "Fetch a URL and return its text content.",
	Parameters: object(
		map[string]any{
			"url": stringProp("Absolute http(s) URL to fetch."),
		},
		"url",
	),
}

type webSearchArgs struct {
	Query string `json:"query"`
}

func webSearch(p *Policy) Tool {
	return Func[webSearchArgs](webSearchSpec, func(ctx context.Context, a webSearchArgs) (Result, error) {
		if err := p.guard(ctx, Request{Tool: "web_search", Access: AccessNetwork, Target: a.Query}); err != nil {
			return Result{}, err
		}
		out, err := duckSearch(ctx, a.Query)
		if err != nil {
			return Result{}, err
		}
		return Result{Output: out}, nil
	})
}

type webFetchArgs struct {
	URL string `json:"url"`
}

func webFetch(p *Policy) Tool {
	return Func[webFetchArgs](webFetchSpec, func(ctx context.Context, a webFetchArgs) (Result, error) {
		if err := p.guard(ctx, Request{Tool: "web_fetch", Access: AccessNetwork, Target: a.URL}); err != nil {
			return Result{}, err
		}
		out, err := fetchText(ctx, a.URL)
		if err != nil {
			return Result{}, err
		}
		return Result{Output: out}, nil
	})
}

// duckSearch scrapes DuckDuckGo's HTML endpoint. It is a minimal fallback: no API
// key, and good enough to return titles, URLs and snippets.
func duckSearch(ctx context.Context, query string) (string, error) {
	u := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", searchUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("search returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return "", err
	}

	text := string(body)
	links := ddgLink.FindAllStringSubmatch(text, 8)
	snips := ddgSnippet.FindAllStringSubmatch(text, 8)
	if len(links) == 0 {
		return "no results (or the search markup changed)", nil
	}

	var b strings.Builder
	for i := range links {
		href := redirectTarget(links[i][1])
		snippet := ""
		if i < len(snips) {
			snippet = clean(snips[i][1])
		}
		fmt.Fprintf(&b, "%d. %s\n%s\n%s\n\n", i+1, clean(links[i][2]), href, snippet)
	}
	return strings.TrimSpace(b.String()), nil
}

func fetchText(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", searchUserAgent)
	req.Header.Set("Accept", "text/html,text/plain;q=0.9,*/*;q=0.5")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return "", err
	}
	return clean(string(body)), nil
}

// redirectTarget extracts the real destination from a DuckDuckGo redirect URL.
func redirectTarget(href string) string {
	if !strings.Contains(href, "uddg=") {
		return href
	}
	start := strings.Index(href, "uddg=") + len("uddg=")
	end := len(href)
	if i := strings.IndexByte(href[start:], '&'); i >= 0 {
		end = start + i
	}
	target, err := url.QueryUnescape(href[start:end])
	if err != nil {
		return href
	}
	return target
}

// clean strips tags, unescapes entities, and collapses whitespace.
func clean(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}
