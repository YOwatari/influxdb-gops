package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/influxdata/influxdb/client/v2"
)

type Metric struct {
	Type      interface{}
	FieldsKey string

	Name   string
	Fields map[string]interface{}
}

func (m *Metric) SetFields(v string) {
	var any interface{}

	switch m.Type.(type) {
	case int:
		any, _ = strconv.ParseFloat(v, 64)
	case []byte:
		any, _ = strconv.ParseFloat(strings.Split(reBytes.FindString(v), " ")[0], 64)
	case time.Time:
		t, _ := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", v)
		any = float64(t.UnixNano()) / 1000. / 1000. / 1000.
	case time.Duration:
		t, _ := time.ParseDuration(v)
		any = float64(t.Nanoseconds()) / 1000. / 1000. / 1000.
	default:
	}

	m.Fields = map[string]interface{}{m.FieldsKey: any}
}

var (
	reBytes    = regexp.MustCompile("[0-9]+ bytes")
	MemMetrics = map[string]Metric{
		"alloc":         Metric{Type: []byte{}, Name: "go_memstats_alloc_bytes", FieldsKey: "gauge"},
		"total-alloc":   Metric{Type: []byte{}, Name: "go_memstats_alloc_bytes_total", FieldsKey: "counter"},
		"sys":           Metric{Type: []byte{}, Name: "go_memstats_sys_bytes", FieldsKey: "gauge"},
		"lookups":       Metric{Type: int(0), Name: "go_memstats_lookups_total", FieldsKey: "counter"},
		"mallocs":       Metric{Type: int(0), Name: "go_memstats_mallocs_total", FieldsKey: "counter"},
		"frees":         Metric{Type: int(0), Name: "go_memstats_frees_total", FieldsKey: "counter"},
		"heap-alloc":    Metric{Type: []byte{}, Name: "go_memstats_heap_alloc_bytes", FieldsKey: "gauge"},
		"heap-sys":      Metric{Type: []byte{}, Name: "go_memstats_heap_sys_bytes", FieldsKey: "gauge"},
		"heap-idle":     Metric{Type: []byte{}, Name: "go_memstats_heap_idle_bytes", FieldsKey: "gauge"},
		"heap-in-use":   Metric{Type: []byte{}, Name: "go_memstats_heap_inuse_bytes", FieldsKey: "gauge"},
		"heap-released": Metric{Type: []byte{}, Name: "go_memstats_heap_released_bytes", FieldsKey: "gauge"},
		"heap-objects":  Metric{Type: int(0), Name: "go_memstats_objects", FieldsKey: "gauge"},
		"stack-in-use":  Metric{Type: []byte{}, Name: "go_memstats_stack_inuse_bytes", FieldsKey: "gauge"},
		"stack-sys":     Metric{Type: []byte{}, Name: "go_memstats_stack_sys_bytes", FieldsKey: "gauge"},
		"gc-pause":      Metric{Type: time.Duration(0), Name: "go_gc_duration_seconds", FieldsKey: "sum"},
		"next-gc":       Metric{Type: []byte{}, Name: "go_memstats_next_gc_bytes", FieldsKey: "gauge"},
		"last-gc":       Metric{Type: time.Now(), Name: "go_memstats_last_gc_time_seconds", FieldsKey: "gauge"},
		"num-gc":        Metric{Type: int(0), Name: "go_gc_duration_seconds", FieldsKey: "count"},
	}
	StatsMetrics = map[string]Metric{
		"goroutines": Metric{Type: int(0), Name: "go_goroutines", FieldsKey: "gauge"},
		"OS threads": Metric{Type: int(0), Name: "go_threads", FieldsKey: "gauge"},
	}

	proc     = flag.Int("p", 0, "process name")
	host     = flag.String("host", "", "influxdb server addr:port")
	database = flag.String("db", "", "influxdb database name")
	interval = flag.Duration("interval", 1*time.Second, "interval time duration")

	exitCode = 2
)

func main() {
	exitCode, err := _main()
	if err != nil {
		log.Print(err)
	}

	os.Exit(exitCode)
}

func _main() (exitCode int, err error) {
	exitCode = 2
	err = fmt.Errorf("unkown error")

	flag.Parse()
	if *proc == 0 || *host == "" || *database == "" {
		flag.Usage()
		return
	}

	// influxdb client
	c, err := client.NewHTTPClient(client.HTTPConfig{
		Addr: fmt.Sprintf("http://%s", *host),
	})
	if err != nil {
		return
	}
	defer c.Close()
	bp, err := client.NewBatchPoints(client.BatchPointsConfig{
		Database:  *database,
		Precision: "s",
	})
	if err != nil {
		return
	}

	for {
		time.Sleep(*interval)

		memstats, err := execGops("memstats", *proc)
		if err != nil {
			if !isRunning(*proc) {
				break
			}
			return exitCode, fmt.Errorf("gops memstats: %s", err.Error())
		}

		stats, err := execGops("stats", *proc)
		if err != nil {
			if !isRunning(*proc) {
				break
			}
			return exitCode, fmt.Errorf("gops stats: %s", err.Error())
		}

		mp := memstatsPoints(memstats)
		sp := statsPoints(stats)
		pts := append(mp, sp...)

		bp.AddPoints(pts)

		c.Write(bp)
	}

	exitCode = 0
	err = nil
	return
}

func isRunning(pid int) bool {
	ok := true

	if err := exec.Command("ps", fmt.Sprint(pid)).Run(); err != nil {
		ok = false
	}

	return ok
}

func execGops(subcmd string, pid int) ([]string, error) {
	b, err := exec.Command("gops", subcmd, fmt.Sprint(pid)).CombinedOutput()
	if err != nil {
		log.Fatal(err)
	}
	return strings.Split(string(b), "\n"), err
}

func memstatsPoints(lines []string) []*client.Point {
	var pts []*client.Point
	var fs []map[string]interface{}

	hostname, _ := os.Hostname()
	tags := map[string]string{"host": hostname}

	for _, line := range lines {
		tokens := strings.SplitN(line, ":", 2)
		if len(tokens) != 2 {
			continue
		}
		name := tokens[0]

		m, ok := MemMetrics[name]
		if !ok {
			continue
		}

		m.SetFields(strings.TrimSpace(tokens[1]))
		pt, _ := client.NewPoint(m.Name, tags, m.Fields, time.Now())

		// gcに関するfieldsを保持する
		if pt.Name() == "go_gc_duration_seconds" {
			f, _ := pt.Fields()
			fs = append(fs, f)
		} else {
			pts = append(pts, pt)
		}
	}

	f := make(map[string]interface{})
	for _, vv := range fs {
		for k, v := range vv {
			f[k] = v
		}
	}
	pt, _ := client.NewPoint("go_gc_duration_seconds", tags, f, time.Now())
	return append(pts, pt)
}

func statsPoints(lines []string) []*client.Point {
	var pts []*client.Point

	hostname, _ := os.Hostname()
	tags := map[string]string{"host": hostname}

	for _, line := range lines {
		tokens := strings.SplitN(line, ":", 2)
		if len(tokens) != 2 {
			continue
		}
		name := tokens[0]

		m, ok := StatsMetrics[name]
		if !ok {
			continue
		}

		m.SetFields(strings.TrimSpace(tokens[1]))
		pt, _ := client.NewPoint(m.Name, tags, m.Fields, time.Now())

		pts = append(pts, pt)
	}

	return pts
}
