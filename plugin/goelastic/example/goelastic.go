package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/elastic/go-elasticsearch/v7"
	"github.com/elastic/go-elasticsearch/v7/esapi"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/goelastic"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
)

// The example below is referred from
// https://github.com/elastic/go-elasticsearch/blob/master/_examples/main.go.

func elasticTest(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	log.SetFlags(0)

	es, err := elasticsearch.NewClient(
		elasticsearch.Config{
			Transport: ppgoelastic.NewTransport(nil),
		})
	if err != nil {
		log.Fatalf("Error creating the client: %s", err)
	}

	indexDocument(ctx, es)
	buf := searchDocument(ctx, es)

	io.WriteString(w, buf.String())
}

func indexDocument(ctx context.Context, es *elasticsearch.Client) {
	var wg sync.WaitGroup
	tracer := pinpoint.FromContext(ctx)

	for i, title := range []string{"Test One", "Test Two"} {
		wg.Add(1)

		go func(i int, title string, asyncTracer pinpoint.Tracer) {
			defer wg.Done()
			defer asyncTracer.EndSpan() //!!must be called
			defer asyncTracer.NewSpanEvent("indexRequest").EndSpanEvent()

			// Build the request body.
			var b strings.Builder
			b.WriteString(`{"title" : "`)
			b.WriteString(title)
			b.WriteString(`"}`)

			// Set up the request object.
			req := esapi.IndexRequest{
				Index:      "test",
				DocumentID: strconv.Itoa(i + 1),
				Body:       strings.NewReader(b.String()),
				Refresh:    "true",
			}

			// Perform the request with the client.
			res, err := req.Do(pinpoint.NewContext(context.Background(), asyncTracer), es)
			if err != nil {
				log.Fatalf("Error getting response: %s", err)
			}
			defer res.Body.Close()

			if res.IsError() {
				log.Printf("[%s] Error indexing document ID=%d", res.Status(), i+1)
			} else {
				// Deserialize the response into a map.
				var r map[string]interface{}
				if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
					log.Printf("Error parsing the response body: %s", err)
				} else {
					// Print the response status and indexed document version.
					log.Printf("[%s] %s; version=%d", res.Status(), r["result"], int(r["_version"].(float64)))
				}
			}
		}(i, title, tracer.NewGoroutineTracer())
	}
	wg.Wait()
}

func searchDocument(ctx context.Context, es *elasticsearch.Client) bytes.Buffer {
	var buf bytes.Buffer
	query := map[string]interface{}{
		"query": map[string]interface{}{
			"match": map[string]interface{}{
				"title": "test",
			},
		},
	}
	if err := json.NewEncoder(&buf).Encode(query); err != nil {
		log.Fatalf("Error encoding query: %s", err)
	}

	var zbuf bytes.Buffer
	gz := gzip.NewWriter(&zbuf)
	gz.Write(buf.Bytes())
	gz.Close()

	// Perform the search request.
	res, err := es.Search(
		es.Search.WithContext(ctx),
		es.Search.WithIndex("test"),
		es.Search.WithHeader(map[string]string{"Content-Encoding": "gzip"}),
		es.Search.WithBody(&zbuf),
		es.Search.WithTrackTotalHits(true),
		es.Search.WithPretty(),
	)
	if err != nil {
		log.Fatalf("Error getting response: %s", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		var e map[string]interface{}
		if err := json.NewDecoder(res.Body).Decode(&e); err != nil {
			log.Fatalf("Error parsing the response body: %s", err)
		} else {
			// Print the response status and error information.
			log.Fatalf("[%s] %s: %s",
				res.Status(),
				e["error"].(map[string]interface{})["type"],
				e["error"].(map[string]interface{})["reason"],
			)
		}
	}

	var r map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		log.Fatalf("Error parsing the response body: %s", err)
	}
	js, _ := json.MarshalIndent(r, "", "    ")

	buf.Reset()
	fmt.Fprintf(&buf, "[%s]\n %s\n\n", res.Status(), string(js))
	for _, hit := range r["hits"].(map[string]interface{})["hits"].([]interface{}) {
		fmt.Fprintf(&buf, "ID=%s, %s\n", hit.(map[string]interface{})["_id"], hit.(map[string]interface{})["_source"])
	}
	return buf
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoElasticTest"),
		pinpoint.WithConfigFile(os.Getenv("HOME") + "/tmp/pinpoint-config.yaml"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}
	defer agent.Shutdown()

	http.HandleFunc("/elastic", pphttp.WrapHandlerFunc(elasticTest))
	http.ListenAndServe(":9000", nil)
}
