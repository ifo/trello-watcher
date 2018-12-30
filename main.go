// trello watcher is a very opinionated helper system using the trello api via trel.
//
// Currently, the board looks like this:
// ----------------------------------------------
// | Projects | Active | To Do | Done | Storage |
// ----------------------------------------------
//
// Projects contains all potential or past project ideas.
// Active contains the currently active project or projects.
// To Do and Done are for subtasks related to the Active project(s).
// Storage is for keeping information stored but out of the way.
//
// Startup involves:
// - Fetching all trel.List resources.
// - Ensuring there is a webhook on the Active list.
// - Ensuring all cards on the active board have an active webhook.
//
// Watching involves:
// - Finding and moving cards when checklist items are completed.
// - Completing checklist items when cards are moved.
// - Storing and retrieving cards when projects are made active or inactive.
// - Adding webhooks to cards moved to the active board.
package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
)

const logLoc = "./log/"

var logger *log.Logger

// The capture names exist only as documentation. They are otherwise unused.
var regex = regexp.MustCompile(".*/(?P<objType>.*)/(?P<objID>.*)/?$")

func main() {
	logTmp, err := ioutil.TempFile(logLoc, "log_*.log")
	if err != nil {
		log.Fatal(err)
	}
	logger = log.New(logTmp, "", log.Ldate|log.Ltime|log.Lshortfile)

	port := os.Getenv("PORT")

	http.HandleFunc("/", index)
	logger.Println("Starting server...")
	logger.Fatalln(http.ListenAndServe(":"+port, nil))
}

func index(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		// Write a file letting us know this route was activated.
		safePath := strings.Replace(r.URL.Path, "/", "_", -1)
		defer ioutil.WriteFile(logLoc+"activated-"+safePath, nil, 0644)
		// A 200 is required to succeed Trello's webhook check.
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		logger.Printf("Received an unsupported method: %s\n", r.Method)
		http.Error(w, "", http.StatusMethodNotAllowed)
	}

	// The last element in the path is the object id.
	// The element before is the object type.
	captures := regex.FindStringSubmatch(r.URL.Path)
	// We always expect at least 3 elements, the full match and at least 2 submatches.
	if len(captures) < 3 {
		logger.Printf("Fewer captures than expected. Found: %v, from path: %s\n", captures, r.URL.Path)
		http.NotFound(w, r)
		return
	}

	l := len(captures)
	objType := captures[l-2]
	objID := captures[l-1]

	// For now write a file containing the response received for the item.
	f, err := ioutil.TempFile(logLoc, objType+"_"+objID+"_")
	if err != nil {
		logger.Println(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	// Record response.
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		logger.Println(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if _, err := f.Write(body); err != nil {
		logger.Println(err)
		f.Close()
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if err := f.Close(); err != nil {
		logger.Println(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type ListChange struct {
	Model struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"model"`
	Action struct {
		Data struct {
			Card struct {
				ID     string `json:"id"`
				IDList string `json:"idList"`
				Name   string `json:"name"`
			} `json:"card"`
			Old struct {
				IDList string `json:"idList"`
			} `json:"old"`
		} `json:"data"`
	} `json:"action"`
}
