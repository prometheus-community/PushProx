package main

import (
  "context"
  "log"
  "time"
  "io"
  "fmt"
  "net/http"
)

type Coordinator struct {
}

func main() {
  http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
    // Proxy request
    if r.URL.Host != "" {
      ctx, _ := context.WithTimeout(r.Context(), time.Second * 10)
      client := &http.Client{}
      request := r.WithContext(ctx)
      request.RequestURI = ""
      // TODO: Prevent any "localhost" or rfc1918 requests to our networks
      resp, err := client.Do(request)
      if err != nil {
        log.Println(err)
        http.Error(w, fmt.Sprintf("Error scraping %q: %s", request.URL.String(), err.Error()), 500)
        return
      }
      defer resp.Body.Close()
      for k, v := range resp.Header {
        w.Header()[k] = v
      }
      w.WriteHeader(resp.StatusCode)
      io.Copy(w, resp.Body)
      return
    }

    if r.URL.Path == "/poll" {
      flushable := w.(http.Flusher)  // TODO: be graceful here.
      flushable.Flush()
      fmt.Fprintf(w, "a\n")
      flushable.Flush()
      fmt.Fprintf(w, "bc\n")
      return
    }

    if r.URL.Path == "/push" {
      return
    }

    http.Error(w, "404: Unknown path", 404)
  })

  log.Fatal(http.ListenAndServe(":1234", nil))
}
