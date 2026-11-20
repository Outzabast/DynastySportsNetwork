package web

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The hosted site keeps media in an R2 bucket. The Worker in front of the
// container serves /media/* straight from the bucket and bridges a virtual
// host (Options.MediaURL) for the container's own reads and writes:
// GET <MediaURL>/_manifest.txt lists every media path, PUT <MediaURL>/<path>
// stores a file. The manifest is written by the media sync workflow.

const mediaManifest = "_manifest.txt"

var mediaClient = &http.Client{Timeout: 30 * time.Second}

// fetchMediaManifest reads the bucket's path list.
func fetchMediaManifest(ctx context.Context, base string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+"/"+mediaManifest, nil)
	if err != nil {
		return nil, err
	}
	resp, err := mediaClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("media manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("media manifest: %s", resp.Status)
	}
	var paths []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if p := strings.TrimSpace(sc.Text()); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, sc.Err()
}

// putMedia stores one file in the bucket at rel ("articles/x.png"). size must
// be the exact body length: R2 rejects streamed (chunked) bodies, so the
// request has to carry a Content-Length.
func putMedia(ctx context.Context, base, rel, contentType string, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", strings.TrimRight(base, "/")+"/"+rel, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)
	resp, err := mediaClient.Do(req)
	if err != nil {
		return fmt.Errorf("store media: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("store media: %s", resp.Status)
	}
	return nil
}
