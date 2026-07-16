package main

import (
	"flag"
	"github.com/capt4ce/paragentix/internal/board"
	"log"
	"net/http"
	"os"
	"os/signal"
)

func main() {
	addr := flag.String("addr", env("ADDR", ":8080"), "listen address")
	db := flag.String("db", "sqlite.db", "database path")
	flag.Parse()
	workspace, _ := os.Getwd()
	a, e := board.Open(*db, workspace)
	if e != nil {
		log.Fatal(e)
	}
	defer a.Close()
	s := &http.Server{Addr: *addr, Handler: a.Handler()}
	go func() { c := make(chan os.Signal, 1); signal.Notify(c, os.Interrupt); <-c; s.Close() }()
	log.Printf("job board listening on %s", *addr)
	if e = s.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		log.Fatal(e)
	}
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
