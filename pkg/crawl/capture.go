package crawl

import (
	"context"
	"github.com/CorentinB/Zeno/pkg/utils"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"

	"github.com/CorentinB/Zeno/pkg/queue"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	log "github.com/sirupsen/logrus"
)

func (c *Crawl) captureWithBrowser(ctx context.Context, item *queue.Item) (outlinks []url.URL, err error) {
	// Log requests
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *network.EventResponseReceived:
			if strings.Compare(ev.Response.URL, item.URL.String()) == 0 {
				log.WithFields(log.Fields{
					"status_code": ev.Response.Status,
					"hash": utils.GetSHA1(ev.Response.URL),
					"hop":         item.Hop,
				}).Info(ev.Response.URL)
			} else {
				log.WithFields(log.Fields{
					"type": "asset",
					"status_code": ev.Response.Status,
					"hash": utils.GetSHA1(ev.Response.URL),
					"hop":         item.Hop,
				}).Debug(ev.Response.URL)
			}
		}
	})


	// Run task
	err = chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(item.URL.String()),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if c.MaxHops > 0 {
				// Extract outer HTML
				node, err := dom.GetDocument().Do(ctx)
				if err != nil {
					return err
				}
				str, err := dom.GetOuterHTML().WithNodeID(node.NodeID).Do(ctx)
				if err != nil {
					return err
				}

				// Extract outlinks
				outlinks = extractOutlinks(str)

				return err
			}

			return err
		}),
	)
	if err != nil {
		return outlinks, err
	}

	return outlinks, nil
}

func (c *Crawl) captureWithGET(ctx context.Context, item *queue.Item) (outlinks []url.URL, err error) {
	// Execute GET request
	resp, err := http.Get(item.URL.String())
	if err != nil {
		return outlinks, err
	}

	log.WithFields(log.Fields{
		"rate": c.URLsPerSecond.Rate(),
		"status_code": resp.StatusCode,
		"hash": item.Hash,
		"hop":         item.Hop,
	}).Info(item.URL.String())

	// Read body
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return outlinks, err
	}

	// Extract outlinks
	outlinks = extractOutlinks(string(body))

	return outlinks, nil
}

// Capture capture a page and queue the outlinks
func (c *Crawl) Capture(ctx context.Context, item *queue.Item) (outlinks []url.URL, err error) {
	// Check with HTTP HEAD request if the URL need a full headless browser or a simple GET request
	if needBrowser(item) && c.Headless == true {
		outlinks, err = c.captureWithBrowser(ctx, item)
	} else {
		outlinks, err = c.captureWithGET(ctx, item)
	}
	c.URLsPerSecond.Incr(1)

	if err != nil {
		return nil, err
	}

	return outlinks, nil
}
