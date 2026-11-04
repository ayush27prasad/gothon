// Command csvstats is an HTTP service that describes CSV files with pandas,
// running on a pool of Python workers managed by gothon.
//
//	curl --data-binary @data.csv localhost:8080/describe
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ayush27prasad/gothon"
	"github.com/ayush27prasad/gothon/examples/csvstats/internal/py/stats"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	rt := gothon.NewRuntime(gothon.Defaults{})
	py, err := stats.Start(ctx, rt, stats.Config{
		Module: gothon.ModuleConfig{
			Workers:   4,
			MaxMemory: 512 << 20, // replace a worker that a huge file left bloated
		},
		Tasks: stats.TaskConfigs{
			Describe: gothon.TaskConfig{Timeout: 5 * time.Second, MaxQueue: 32},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("POST /describe", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		out, err := py.Describe(r.Context(), stats.DescribeIn{CSV: string(body)})
		var pe *gothon.PythonError
		switch {
		case err == nil:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(out)
		case errors.Is(err, gothon.ErrBusy):
			http.Error(w, "busy, try again", http.StatusServiceUnavailable)
		case errors.Is(err, context.DeadlineExceeded):
			http.Error(w, "timed out", http.StatusGatewayTimeout)
		case errors.As(err, &pe) && (pe.Stage == gothon.StageInput || pe.Type == "ValueError"):
			http.Error(w, pe.Message, http.StatusBadRequest)
		default:
			log.Printf("describe: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	})
	http.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(rt.Stats())
	})

	srv := &http.Server{Addr: ":8080"}
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	log.Print("listening on :8080")

	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	srv.Shutdown(shutdown) // stop taking requests first, then let Python finish
	rt.Shutdown(shutdown)
}
