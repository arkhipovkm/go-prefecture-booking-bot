package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"golang.org/x/net/html"
)

var (
	TELEGRAM_BOT_API_TOKEN         string
	BOT                            *tgbotapi.BotAPI
	LAST_SCHEDULE_CHECK            *time.Time
	ENDPOINT                       string
	PROXY                          string
	PLANNING_ID_TO_COMMON_NAME_MAP = map[string]string{
		"41132": "Guichet 7",
		"41134": "Guichet 9",
	}
)

func loadSubscrierIDs() ([]int64, error) {
	var err error
	var subscriberIDs []int64
	file, err := os.Open("subscriber_ids.txt")
	if err != nil {
		return subscriberIDs, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		subscriberID, err := strconv.Atoi(scanner.Text())
		if err != nil {
			return subscriberIDs, err
		}
		subscriberIDs = append(subscriberIDs, int64(subscriberID))
	}

	if err := scanner.Err(); err != nil {
		return subscriberIDs, err
	}

	return subscriberIDs, err
}

func saveSubscrierIDs(subscriberIDs []int64) error {
	var err error
	file, err := os.Create("subscriber_ids.txt")
	if err != nil {
		return err
	}
	defer file.Close()

	for _, subscriberID := range subscriberIDs {
		_, err := file.WriteString(strconv.FormatInt(subscriberID, 10) + "\n")
		if err != nil {
			return err
		}
	}

	return err
}

func init() {
	TELEGRAM_BOT_API_TOKEN = os.Getenv("TELEGRAM_BOT_API_TOKEN")
	if TELEGRAM_BOT_API_TOKEN == "" {
		panic("No TELEGRAM_BOT_API_TOKEN in the environment")
	}
	log.Printf("TELEGRAM_BOT_API_TOKEN: %s", TELEGRAM_BOT_API_TOKEN)

	ENDPOINT = os.Getenv("ENDPOINT")
	if ENDPOINT == "" {
		panic("No ENDPOINT in the environment")
	}
	log.Printf("ENDPOINT: %s", ENDPOINT)

	PROXY = os.Getenv("PROXY")
	if PROXY != "" {
		log.Printf("PROXY: %s", PROXY)
	} else {
		log.Printf("PROXY: not set")
	}

	// check if subscribers file exists
	if _, err := os.Stat("subscriber_ids.txt"); os.IsNotExist(err) {
		_, err := os.Create("subscriber_ids.txt")
		if err != nil {
			panic(err)
		}
	}

	// configure time zone
	// loc, err := time.LoadLocation("Europe/Paris")
	// if err != nil {
	// 	panic(err)
	// }
	// time.Local = loc

}

func doHTTPPostRequest(uri string, form url.Values, client *http.Client) ([]byte, error) {
	var err error
	var body []byte

	resp, err := client.PostForm(uri, form)
	if err != nil {
		return body, err
	}

	if resp.StatusCode != 200 {
		err = fmt.Errorf("bad status: %s", resp.Status)
		return body, err
	}

	// Print all received cookies
	for _, cookie := range resp.Cookies() {
		log.Printf("Cookie: %s : %s\n", cookie.Name, cookie.Value)
	}

	defer resp.Body.Close()

	body, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		return body, err
	}

	return body, err
}

func initializeSession(uri string) (*http.Client, error) {

	log.Println("Initializing session...")

	var err error
	jar, err := cookiejar.New(nil)
	if err != nil {
		log.Fatalf("Got error while creating cookie jar %s", err.Error())
	}

	var tr *http.Transport
	if PROXY != "" {
		log.Printf("Using proxy %s", PROXY)
		tr = &http.Transport{
			Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: PROXY}),
		}
	}
	client := &http.Client{
		Jar:       jar,
		Transport: tr,
	}

	req, err := http.NewRequest("GET", uri, nil)
	if err != nil {
		return client, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return client, err
	}

	if resp.StatusCode != 200 {
		err = fmt.Errorf("bad status: %s", resp.Status)
		return client, err
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return client, err
	}

	var sessionID string
	for _, cookie := range client.Jar.Cookies(req.URL) {
		log.Printf("Initializing session. Cookie: %s : %s\n", cookie.Name, cookie.Value)
		if cookie.Name == "eZSESSID" {
			sessionID = cookie.Value
		}
	}

	if sessionID == "" {
		err = errors.New("no session ID found")
		log.Println(string(body))
		return client, err
	}

	return client, err

}

func htmlNodeToString(n *html.Node) string {
	var str string
	var buf bytes.Buffer
	w := io.Writer(&buf)
	html.Render(w, n)
	str = buf.String()
	return str
}

func htmlNodeGetChildrenTExt(n *html.Node) string {
	var str string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			str += c.Data
		}
	}
	return strings.TrimSpace(str)
}

func parseHTML(body []byte) (string, error) {
	var err error
	var str string
	// Recursively parse the HTML tree
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "form" {
			for _, a := range n.Attr {
				if a.Key == "name" && a.Val == "create" {
					str = htmlNodeGetChildrenTExt(n)
					break
				}
			}
		}
		// Recursion
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	// First call of recursive function
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return str, err
	}
	f(doc)

	return str, err

}

func contains(s []int64, e int64) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func processTelegramUpdates(bot *tgbotapi.BotAPI, updates tgbotapi.UpdatesChannel) {
	for update := range updates {
		if update.Message == nil {
			continue
		}
		var err error = nil
		switch update.Message.Command() {
		case "subscribe":
			subscriberIDs, err := loadSubscrierIDs()
			if err != nil {
				log.Printf("Got error while loading subscriber IDs %s", err.Error())
			}
			if !contains(subscriberIDs, update.Message.Chat.ID) {
				subscriberIDs = append(subscriberIDs, update.Message.Chat.ID)
				err = saveSubscrierIDs(subscriberIDs)
				if err != nil {
					log.Printf("Got error while saving subscriber IDs %s", err.Error())
				}
				msg := tgbotapi.NewMessage(
					update.Message.Chat.ID,
					"You have been subscribed to the notifications",
				)
				_, err = BOT.Send(msg)
				if err != nil {
					log.Printf("Got error while sending message to subscriber %d %s", update.Message.Chat.ID, err.Error())
				}
			} else {
				msg := tgbotapi.NewMessage(
					update.Message.Chat.ID,
					"You are already subscribed to the notifications",
				)
				_, err = BOT.Send(msg)
				if err != nil {
					log.Printf("Got error while sending message to subscriber %d %s", update.Message.Chat.ID, err.Error())
				}
			}
		case "unsubscribe":
			subscriberIDs, err := loadSubscrierIDs()
			if err != nil {
				log.Printf("Got error while loading subscriber IDs %s", err.Error())
			}
			if contains(subscriberIDs, update.Message.Chat.ID) {
				var newSubscriberIDs []int64
				for _, subscriberID := range subscriberIDs {
					if subscriberID != update.Message.Chat.ID {
						newSubscriberIDs = append(newSubscriberIDs, subscriberID)
					}
				}
				err = saveSubscrierIDs(newSubscriberIDs)
				if err != nil {
					log.Printf("Got error while saving subscriber IDs %s", err.Error())
				}
				msg := tgbotapi.NewMessage(
					update.Message.Chat.ID,
					"You have been unsubscribed from the notifications",
				)
				_, err = BOT.Send(msg)
				if err != nil {
					log.Printf("Got error while sending message to subscriber %d %s", update.Message.Chat.ID, err.Error())
				}
			} else {
				msg := tgbotapi.NewMessage(
					update.Message.Chat.ID,
					"You are not subscribed to the notifications",
				)
				_, err = BOT.Send(msg)
				if err != nil {
					log.Printf("Got error while sending message to subscriber %d %s", update.Message.Chat.ID, err.Error())
				}
			}
		case "start":
			msg := tgbotapi.NewMessage(
				update.Message.Chat.ID,
				"Welcome to the Prefecture appointment notifier bot. You can subscribe to the notifications using the /subscribe command and unsubscribe using the /unsubscribe command.",
			)
			_, err = BOT.Send(msg)
			if err != nil {
				log.Printf("Got error while sending message to subscriber %d %s", update.Message.Chat.ID, err.Error())
			}
		case "status":
			// format LAST_SCHEDULE_CHECK to string using current time zone
			lastScheduleCheckStr := LAST_SCHEDULE_CHECK.Format("2006-01-02 15:04:05")
			msg := tgbotapi.NewMessage(
				update.Message.Chat.ID,
				"Bot is running. Last schedule check was at "+lastScheduleCheckStr,
			)
			_, err = BOT.Send(msg)
			if err != nil {
				log.Printf("Got error while sending message to subscriber %d %s", update.Message.Chat.ID, err.Error())
			}
		default:
			msg := tgbotapi.NewMessage(
				update.Message.Chat.ID,
				"Unknown command",
			)
			_, err = BOT.Send(msg)
			if err != nil {
				log.Printf("Got error while sending message to subscriber %d %s", update.Message.Chat.ID, err.Error())
			}
		}
	}
}

func initializeTelegramBot() error {
	var err error
	BOT, err = tgbotapi.NewBotAPI(os.Getenv("TELEGRAM_BOT_API_TOKEN"))
	if err != nil {
		return err
	}

	debug := false
	debugEnv := os.Getenv("DEBUG")
	if debugEnv != "" {
		debug = true
	}

	BOT.Debug = debug
	log.Printf("Authenticated on Telegram Bot account %s", BOT.Self.UserName)

	_, err = BOT.RemoveWebhook()
	if err != nil {
		return err
	}
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates, err := BOT.GetUpdatesChan(tgbotapi.NewUpdate(0))
	if err != nil {
		return err
	}

	for w := 0; w < runtime.NumCPU()+2; w++ {
		go processTelegramUpdates(BOT, updates)
	}

	return err
}

func main() {

	err := initializeTelegramBot()
	if err != nil {
		log.Fatal(err)
	}

	client, err := initializeSession(ENDPOINT)
	if err != nil {
		log.Fatal(err)
	}

	t0 := time.Now()

	for {
		for planningID := range PLANNING_ID_TO_COMMON_NAME_MAP {
			form := url.Values{
				"planning":   {planningID},
				"nextButton": {"Etape+suivante"},
			}
			body, err := doHTTPPostRequest(ENDPOINT, form, client)
			if err != nil {
				log.Fatal(err)
			}
			str, err := parseHTML(body)
			if err != nil {
				log.Fatal(err)
			}
			log.Printf("Planning ID: %s, Result: %s\n", planningID, str)
			t := time.Now()
			LAST_SCHEDULE_CHECK = &t

			if str == "" {
				log.Printf("Got empty result for planning ID %s", planningID)
				continue
			}

			if !strings.Contains(str, "Il n'existe plus de plage horaire libre") {
				subscriberIDs, err := loadSubscrierIDs()
				if err != nil {
					log.Printf("Got error while loading subscriber IDs %s", err.Error())
				}
				for _, subscriberID := range subscriberIDs {
					msg := tgbotapi.NewMessage(
						subscriberID,
						"An appointment is available for "+PLANNING_ID_TO_COMMON_NAME_MAP[planningID],
					)
					_, err = BOT.Send(msg)
					if err != nil {
						log.Printf("Got error while sending message to subscriber %d %s", subscriberID, err.Error())
					}
				}
			}

			time.Sleep(30 * time.Second)
		}

		if time.Since(t0) > 30*time.Minute {
			client, err = initializeSession(ENDPOINT)
			if err != nil {
				log.Fatal(err)
			}
			t0 = time.Now()
		}
	}
}
