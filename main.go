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
	"time"

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
	Webhooks trel.Webhooks
}

func init() {
	// Setup logging.
	logTmp, err := ioutil.TempFile(logLoc, "log_*.log")
	if err != nil {
		log.Fatal(err)
	}
	logger = log.New(logTmp, "", log.Ldate|log.Ltime|log.Lshortfile)
	fmt.Printf("logging to file: %s\n", logTmp.Name())

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
		logger.Println(err)
		logger.Fatalln("Failed to retrieve board lists")
	}

	listNames := []string{"Projects", "Active", "To Do", "Done", "Storage"}
	lm := map[string]trel.List{}
	for _, name := range listNames {
		l, err := lists.FindList(name)
		if err != nil {
			logger.Println(err)
			logger.Fatalf("The board needs a list named %q\n", name)
		}
		lm[name] = l
	}

	webhooks, err := trelClient.Webhooks()
	if err != nil {
		logger.Println(err)
		logger.Fatalln("Unable to retrieve webhooks")
	}

	// Setup the board global.
	board = Board{
		Projects: lm["Projects"],
		Active:   lm["Active"],
		ToDo:     lm["To Do"],
		Done:     lm["Done"],
		Storage:  lm["Storage"],
		Webhooks: webhooks,
	}
}

func main() {
	// Give the server a second to start before creating webhooks.
	go func() {
		time.Sleep(1 * time.Second)
		SetupInitialWebhooks()
		cards, err := board.Active.Cards()
		if err != nil {
			logger.Fatalf("Unable to fetch active cards: %s\n", err)
		}
		for _, card := range cards {
			SetupActiveProjectCard(card)
		}
	}()

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
		return
	}

	// The last element in the path is the object id.
	// The element before is the object type.
	captures := regex.FindStringSubmatch(r.URL.Path)
	// We always expect 3 elements, the full match and 2 submatches.
	if len(captures) != 3 {
		logger.Printf("Too many or too few captures. Found: %v, from path: %s\n", captures, r.URL.Path)
		http.NotFound(w, r)
		return
	}

	isValidCapture := false
	for _, t := range []string{"list", "card"} {
		if captures[1] == t {
			isValidCapture = true
		}
	}

	if !isValidCapture {
		logger.Printf("Invalid captures: %s\n", r.URL.Path)
		http.NotFound(w, r)
		return
	}

	objType := captures[1]
	objID := captures[2]

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

func SetupActiveProjectCard(card trel.Card) error {
	if !HasWebhook(card.ID, board.Webhooks) {
		wh, err := DefaultWebhook(trelClient, "card", card.ID)
		if err != nil {
			return err
		}
		board.Webhooks = append(board.Webhooks, wh)
	}

	checklists, err := card.Checklists()
	if err != nil {
		return err
	}

	cards, err := board.Storage.Cards()
	if err != nil {
		return err
	}

	for _, cl := range checklists {
		for _, ci := range cl.CheckItems {
			// Either find the card and move it, or make one.
			c, err := cards.FindCard(ci.Name)
			if _, ok := err.(trel.NotFoundError); ok {
				// Make the card.
				list := board.ToDo
				if ci.State == "complete" {
					list = board.Done
				}
				_, cardErr := list.NewCard(ci.Name, "", "")
				if cardErr != nil {
					return err
				}
			} else {
				// Move the card.
				list := board.ToDo
				if ci.State == "complete" {
					list = board.Done
				}
				err := c.Move(list.ID)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func StoreInactiveProjectCard(card trel.Card) error {
	// Move all cards to storage
	checklists, err := card.Checklists()
	if err != nil {
		return err
	}

	// Collect all cards on the To Do and Done boards.
	todoCards, err := board.ToDo.Cards()
	if err != nil {
		return err
	}
	doneCards, err := board.Done.Cards()
	if err != nil {
		return err
	}
	cards := append(todoCards, doneCards...)

	for _, cl := range checklists {
		for _, ci := range cl.CheckItems {
			c, err := cards.FindCard(ci.Name)
			if _, ok := err.(trel.NotFoundError); ok {
				// Ignore cards that are missing.
				// They will be created later if this project becomes active again.
				continue
			}
			// Move the card.
			err = c.Move(board.Storage.ID)
			if err != nil {
				return err
			}
		}
	}

	// Deactivate this card's webhook if it exists.
	webhook := FindWebhook(card.ID, board.Webhooks)
	if webhook.ID == "" {
		// Ignore webhooks that are missing.
		return nil
	}
	return webhook.Deactivate()
}

type ListChange struct {
	Model struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"model"`
	Action struct {
		Type string `json:"type"` // "updateCard"
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

type CheckItemChange struct {
	Model struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"model"`
	Action struct {
		Type string `json:"type"` // "updateCheckItemStateOnCard"
		Data struct {
			Card struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"card"`
			CheckItem struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"checkItem"`
			Checklist struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"checklist"`
		} `json:"data"`
	} `json:"action"`
}

func SetupInitialWebhooks() {
	if !HasWebhook(board.Active.ID, board.Webhooks) {
		hook, err := DefaultWebhook(trelClient, "list", board.Active.ID)
		if err != nil {
			logger.Println(err)
			logger.Fatalln("Unable to create Webhook for Active list")
		}
		board.Webhooks = append(board.Webhooks, hook)
	}

	cards, err := board.Active.Cards()
	if err != nil {
		logger.Println(err)
		logger.Fatalln("Unable to get Active list cards")
	}

	for _, card := range cards {
		if !HasWebhook(card.ID, board.Webhooks) {
			hook, err := DefaultWebhook(trelClient, "card", card.ID)
			if err != nil {
				logger.Println(err)
				logger.Fatalf("Unable to create Webhook for Active list card: %s\n", card.ID)
			}
			board.Webhooks = append(board.Webhooks, hook)
		}
	}
}

func HasWebhook(id string, ws trel.Webhooks) bool {
	wh := FindWebhook(id, ws)
	if wh.ID != "" {
		return true
	}
	return false
}

func FindWebhook(id string, ws trel.Webhooks) trel.Webhook {
	for _, w := range ws {
		if w.IDModel == id {
			return w
		}
	}
	return trel.Webhook{}
}

func DefaultWebhook(c *trel.Client, typ, id string) (trel.Webhook, error) {
	cb := DefaultCallbackURL(typ, id)
	return c.NewWebhook(fmt.Sprintf("%s: %s", typ, id), cb, id)
}

func DefaultCallbackURL(typ, id string) string {
	return MakeCallbackURL("https", host, typ, id)
}

func MakeCallbackURL(scheme, host, typ, id string) string {
	u := url.URL{
		Scheme: scheme,
		Host:   host,
		Path:   fmt.Sprintf("/%s/%s", typ, id),
	}
	return u.String()
}
