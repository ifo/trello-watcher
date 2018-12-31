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
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/ifo/trel"
)

const logLoc = "./log/"

// The capture names exist only as documentation. They are otherwise unused.
var regex = regexp.MustCompile(".*/(?P<objType>.*)/(?P<objID>.*)/?$")

var logger *log.Logger
var trelClient *trel.Client
var host = os.Getenv("HOST")
var port = os.Getenv("PORT")
var board Board

type Board struct {
	Projects trel.List
	Active   trel.List
	ToDo     trel.List
	Done     trel.List
	Storage  trel.List
}

func init() {
	// Setup logging.
	logTmp, err := ioutil.TempFile(logLoc, "log_*.log")
	if err != nil {
		log.Fatal(err)
	}
	logger = log.New(logTmp, "", log.Ldate|log.Ltime|log.Lshortfile)

	// Fetch the trello board lists.
	var boardID, key, token string

	pBoardID := flag.String("board", "", "trello board id")
	pKey := flag.String("key", "", "trello api key")
	pToken := flag.String("token", "", "trello api token")
	pHost := flag.String("host", "", "server host name (web address)")
	pPort := flag.String("port", "0", "server port")
	flag.Parse()

	boardID, key, token = *pBoardID, *pKey, *pToken
	if boardID == "" {
		boardID = os.Getenv("TRELLO_BOARD_ID")
	}
	if key == "" {
		key = os.Getenv("TRELLO_KEY")
	}
	if token == "" {
		token = os.Getenv("TRELLO_TOKEN")
	}
	if *pHost != "" {
		host = *pHost
	}
	if *pPort != "0" {
		port = *pPort
	}
	if boardID == "" || key == "" || token == "" || host == "" || port == "0" {
		logger.Fatalln("The Board ID, Trello Key and Token, Host, and Port are all required")
	}

	// We can leave the username empty because we already know the board id.
	trelClient = trel.New("", key, token)
	lists, err := trelClient.Board(boardID)
	if err != nil {
		logger.Fatalln("Failed to retrieve board lists")
	}

	listNames := []string{"Projects", "Active", "To Do", "Done", "Storage"}
	lm := map[string]trel.List{}
	for _, name := range listNames {
		l, err := lists.FindList(name)
		if err != nil {
			logger.Fatalf("The board needs a list named %q\n", name)
		}
		lm[name] = l
	}

	// Setup the board global.
	board = Board{
		Projects: lm["Projects"],
		Active:   lm["Active"],
		ToDo:     lm["To Do"],
		Done:     lm["Done"],
		Storage:  lm["Storage"],
	}

	// TODO:
	// - Get all webhooks
	// - Ensure Active has a webhook
	// - Ensure all cards on Active have a webhook
}

func main() {
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

func MakeCallbackURL(scheme, host, typ, id string) string {
	u := url.URL{
		Scheme: scheme,
		Host:   host,
		Path:   fmt.Sprintf("/%s/%s", typ, id),
	}
	return u.String()
}
