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
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	http.HandleFunc("/webhooks", webhooks)
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

	// Attempt to parse the body.
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		logger.Println(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	if objType == "list" {
		var listChange ListChange
		if err = json.Unmarshal(body, &listChange); err == nil {
			err = listChange.Handle()
			if err != nil {
				logger.Println(err)
				http.Error(w, "", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		} else {
			logger.Println(err)
		}
	}

	if objType == "card" {
		var checkItemChange CheckItemChange
		if err := json.Unmarshal(body, &checkItemChange); err == nil {
			err = checkItemChange.Handle()
			if err != nil {
				logger.Println(err)
				http.Error(w, "", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		} else {
			logger.Println(err)
		}
	}

	// We didn't understand the body, so write a file containing the response received for the item.
	err = RecordResponse(objType, objID, r.Body)
	if err != nil {
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
		Type string `json:"type"` // "updateCard"
		Data struct {
			ListAfter struct {
				ID   string `json:ID`
				Name string `json:"name"`
			} `json:"listAfter"`
			ListBefore struct {
				ID   string `json:ID`
				Name string `json:"name"`
			} `json:"listBefore"`
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

func (lc ListChange) Handle() error {
	logger.Printf("ListChange being handled for card %s\n", lc.Action.Data.Card.ID)
	card, err := trelClient.Card(lc.Action.Data.Card.ID)
	if err != nil {
		return err
	}

	afterName := lc.Action.Data.ListAfter.Name
	beforeName := lc.Action.Data.ListBefore.Name

	// Ignore all moves to and from storage
	if beforeName == board.Storage.Name || afterName == board.Storage.Name {
		return nil
	}

	// The card moved to Active from Projects, so set it up.
	if afterName == board.Active.Name && beforeName == board.Projects.Name {
		return SetupActiveProjectCard(card)
	}
	// The card moved to Projects from Active, so store it.
	if afterName == board.Projects.Name && beforeName == board.Active.Name {
		return StoreInactiveProjectCard(card)
	}

	// The card moved to Done from To Do, so complete the CheckItem.
	if afterName == board.Done.Name && beforeName == board.ToDo.Name {
		if ci, err := FindListCheckItem(board.Active, card.Name); err == nil {
			return ci.Complete()
		} else {
			return err
		}
	}

	// The card moved to To Do from Done, so mark the CheckItem incomplete.
	if afterName == board.ToDo.Name && beforeName == board.Done.Name {
		if ci, err := FindListCheckItem(board.Active, card.Name); err == nil {
			return ci.Incomplete()
		} else {
			return err
		}
	}

	// The card wasn't moved to or from Projects or Active, so don't do anything.
	return nil
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

func (cic CheckItemChange) Handle() error {
	ciName := cic.Action.Data.CheckItem.Name
	ciState := cic.Action.Data.CheckItem.State
	logger.Printf("CheckItemChange made with name %s and state %s\n", ciName, ciState)
	// A CheckItem was marked complete, so move the card to Done.
	if ciState == "complete" {
		card, err := board.ToDo.FindCard(ciName)
		if err != nil {
			return err
		}
		return card.Move(board.Done.ID)
	}

	// A CheckItem was created or marked incomplete, so move it to To Do or make one.
	if ciState == "incomplete" {
		card, err := board.Done.FindCard(ciName)
		if _, ok := err.(trel.NotFoundError); ok {
			// Check to see if the card already exists, and if not, make it.
			if _, err = board.ToDo.FindCard(ciName); err != nil {
				// Make the card, because we did not find it anywhere.
				_, err = board.ToDo.NewCard(ciName, "", "")
			}
			return err
		}
		return card.Move(board.ToDo.ID)
	}
	return nil
}

func RecordResponse(objType, objID string, r io.Reader) error {
	// For now write a file containing the response received for the item.
	f, err := ioutil.TempFile(logLoc, objType+"_"+objID+"_")
	if err != nil {
		return err
	}

	// Record response.
	body, err := ioutil.ReadAll(r)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		defer f.Close()
		return err
	}
	return f.Close()
}

func SetupActiveProjectCard(card trel.Card) error {
	if !HasWebhook(card.ID, board.Webhooks) {
		wh, err := DefaultWebhook(trelClient, "card", card.ID)
		if err != nil {
			return err
		}
		board.Webhooks = append(board.Webhooks, wh)
	}

	// Ensure webhook is active.
	wh, err := board.Webhooks.Find(card.ID)
	if err != nil {
		return err
	}
	if err := wh.Activate(); err != nil {
		return err
	}

	checklists, err := card.Checklists()
	if err != nil {
		return err
	}

	cards, err := board.Storage.Cards()
	if err != nil {
		return err
	}

	todoCards, err := board.ToDo.Cards()
	if err != nil {
		return err
	}

	doneCards, err := board.Done.Cards()
	if err != nil {
		return err
	}

	// Before we load up any cards in the Done list, we need to deactivate the webhook to prevent a bunch of card moving spam.
	if wh, err := board.Webhooks.Find(board.Done.ID); err == nil {
		wh.Deactivate()
	}

	for _, cl := range checklists {

		// If every item in the checklist is complete, skip adding them to the board.
		allComplete := true
		for _, ci := range cl.CheckItems {
			if ci.State == "incomplete" {
				allComplete = false
				break
			}
		}
		if allComplete {
			continue
		}

		for _, ci := range cl.CheckItems {
			// Either find the card and move it, or make one.
			c, err := cards.Find(ci.Name)
			if _, ok := err.(trel.NotFoundError); ok {
				// See if the card exists on another board, otherwise make it.
				if _, err := todoCards.Find(ci.Name); err == nil {
					return nil
				}
				if _, err := doneCards.Find(ci.Name); err == nil {
					return nil
				}
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

	// Reactivate the Done webhook.
	if wh, err := board.Webhooks.Find(board.Done.ID); err == nil {
		wh.Activate()
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

	// Before we remove any cards from the Done list, we need to deactivate the webhook to prevent a bunch of card moving spam.
	if wh, err := board.Webhooks.Find(board.Done.ID); err == nil {
		wh.Deactivate()
	}

	for _, cl := range checklists {
		for _, ci := range cl.CheckItems {
			c, err := cards.Find(ci.Name)
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

	// Reactivate the Done webhook.
	if wh, err := board.Webhooks.Find(board.Done.ID); err == nil {
		wh.Activate()
	}

	// Deactivate this card's webhook if it exists.
	webhook, err := board.Webhooks.Find(card.ID)
	if err != nil {
		logger.Println(err)
		// Ignore webhooks that are missing.
		return nil
	}
	return webhook.Deactivate()
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

	if !HasWebhook(board.Done.ID, board.Webhooks) {
		hook, err := DefaultWebhook(trelClient, "list", board.Done.ID)
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
	_, err := ws.Find(id)
	if err != nil {
		return false
	}
	return true
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

func FindListCheckItem(l trel.List, ciName string) (*trel.CheckItem, error) {
	cards, err := l.Cards()
	if err != nil {
		return nil, err
	}

	for _, c := range cards {
		cls, err := c.Checklists()
		if err != nil {
			return nil, err
		}
		for _, cl := range cls {
			if ci, err := cl.CheckItems.Find(ciName); err == nil {
				return ci, nil
			}
		}
	}

	return nil, trel.NotFoundError{Type: "CheckItem", Identifier: ciName}
}

func webhooks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "", 404)
		return
	}

	for _, wh := range board.Webhooks {
		fmt.Fprintf(w, "%+v\n", wh)
	}
}
