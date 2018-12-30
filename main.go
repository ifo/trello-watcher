package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
)

const LOG_LOC = "./log/"

var logger *log.Logger

// The capture names exist only as documentation. They are otherwise unused.
var regex = regexp.MustCompile(".*/(?P<objType>.*)/(?P<objID>.*)/?$")

func main() {
	logTmp, err := ioutil.TempFile(LOG_LOC, "log_*.log")
	if err != nil {
		log.Fatal(err)
	}
	logger = log.New(logTmp, "", log.Ldate|log.Ltime|log.Lshortfile)

	port := os.Getenv("PORT")

	http.HandleFunc("/", index)
	log.Fatalln(http.ListenAndServe(":"+port, nil))
}

func index(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		// Write a file letting us know this route was activated.
		safePath := strings.Replace(r.URL.Path, "/", "_", -1)
		defer ioutil.WriteFile(LOG_LOC+"activated-"+safePath, nil, 0644)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// The last element in the path is the object id.
	// The element before is the object type.
	captures := regex.FindStringSubmatch(r.URL.Path)
	// We always expect at least 3 elements, the full match and at least 2 submatches.
	if len(captures) < 3 {
		log.Printf("Fewer captures than expected. Found: %v, from path: %s\n", captures, r.URL.Path)
		http.NotFound(w, r)
		return
	}

	l := len(captures)
	objType := captures[l-2]
	objID := captures[l-1]

	// For now write a file containing the response received for the item.
	f, err := ioutil.TempFile(LOG_LOC, objType+"_"+objID)
	if err != nil {
		logger.Println(err)
		http.Error(w, "", 500)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		logger.Println(err)
		http.Error(w, "", 500)
		return
	}
	if _, err := f.Write(body); err != nil {
		logger.Println(err)
		f.Close()
		http.Error(w, "", 500)
		return
	}
	if err := f.Close(); err != nil {
		logger.Println(err)
		http.Error(w, "", 500)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
