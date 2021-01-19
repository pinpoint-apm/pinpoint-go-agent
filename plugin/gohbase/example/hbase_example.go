package main

import (
	"log"
	"net/http"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	phbase "github.com/pinpoint-apm/pinpoint-go-agent/plugin/gohbase"
	phttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	"github.com/tsuna/gohbase/filter"
	"github.com/tsuna/gohbase/hrpc"
)

func doHbase(w http.ResponseWriter, r *http.Request) {
	client := phbase.NewClient("localhost")
	ctx := r.Context()

	values := map[string]map[string][]byte{"cf": {"a": []byte{0}}}
	putRequest, err := hrpc.NewPutStr(ctx, "table", "key", values)
	if err != nil {
		log.Println(err)
	}
	_, err = client.Put(putRequest)
	if err != nil {
		log.Println(err)
	}

	getRequest, err := hrpc.NewGetStr(ctx, "table", "row")
	if err != nil {
		log.Println(err)
	}
	_, err = client.Get(getRequest)
	if err != nil {
		log.Println(err)
	}

	pFilter := filter.NewPrefixFilter([]byte("7"))
	scanRequest, err := hrpc.NewScanStr(ctx, "table", hrpc.Filters(pFilter))
	if err != nil {
		log.Println(err)
	}
	scan := client.Scan(scanRequest)
	scan.Next()
}

func main() {
	opts := []pinpoint.ConfigOption{
		pinpoint.WithAppName("GoHbaseTest"),
		pinpoint.WithAgentId("GoHbaseTestAgent"),
		pinpoint.WithCollectorHost("localhost"),
		pinpoint.WithLogLevel("debug"),
	}
	cfg, _ := pinpoint.NewConfig(opts...)
	agent, err := pinpoint.NewAgent(cfg)
	if err != nil {
		log.Fatalf("pinpoint agent start fail: %v", err)
	}

	http.HandleFunc(phttp.WrapHandleFunc(agent, "doHbase", "/hbase", doHbase))

	http.ListenAndServe(":9000", nil)
	agent.Shutdown()
}
