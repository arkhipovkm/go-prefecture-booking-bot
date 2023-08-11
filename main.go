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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"golang.org/x/net/html"
)

var (
	TELEGRAM_BOT_API_TOKEN     string
	TELEGRAM_BOT_OWNER_CHAT_ID int64
	BOT                        *tgbotapi.BotAPI
	LAST_SCHEDULE_CHECK        *time.Time
	ENDPOINT                   string
	PROXY_HOST                 string
	PROXY_USERNAME             string
	PROXY_PASSWORD             string
	// PLANNING_ID_TO_COMMON_NAME_MAP = map[string]string{
	// 	"41132": "Guichet 7",
	// 	//"41134": "Guichet 9",
	// }
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

func saveFile(filename string, body []byte) error {
	return os.WriteFile(filename, body, os.ModePerm)
}

func init() {
	var err error
	TELEGRAM_BOT_API_TOKEN = os.Getenv("TELEGRAM_BOT_API_TOKEN")
	if TELEGRAM_BOT_API_TOKEN == "" {
		panic("No TELEGRAM_BOT_API_TOKEN in the environment")
	}
	log.Printf("TELEGRAM_BOT_API_TOKEN: %s", TELEGRAM_BOT_API_TOKEN)

	TELEGRAM_BOT_OWNER_CHAT_ID, err = strconv.ParseInt(os.Getenv("TELEGRAM_BOT_OWNER_CHAT_ID"), 10, 64)
	if err != nil {
		panic(err)
	}
	if TELEGRAM_BOT_OWNER_CHAT_ID == 0 {
		panic("No TELEGRAM_BOT_OWNER_CHAT_ID in the environment")
	}
	log.Printf("TELEGRAM_BOT_OWNER_CHAT_ID: %d", TELEGRAM_BOT_OWNER_CHAT_ID)

	ENDPOINT = os.Getenv("ENDPOINT")
	if ENDPOINT == "" {
		panic("No ENDPOINT in the environment")
	}
	log.Printf("ENDPOINT: %s", ENDPOINT)

	PROXY_HOST = os.Getenv("PROXY_HOST")
	if PROXY_HOST != "" {
		log.Printf("PROXY_HOST: %s", PROXY_HOST)
	} else {
		log.Printf("PROXY_HOST: not set")
	}

	PROXY_USERNAME = os.Getenv("PROXY_USERNAME")
	if PROXY_USERNAME != "" {
		log.Printf("PROXY_USERNAME: %s", PROXY_USERNAME)
	} else {
		log.Printf("PROXY_USERNAME: not set")
	}

	PROXY_PASSWORD = os.Getenv("PROXY_PASSWORD")
	if PROXY_PASSWORD != "" {
		log.Printf("PROXY_PASSWORD: %s", PROXY_PASSWORD)
	} else {
		log.Printf("PROXY_PASSWORD: not set")
	}

	t := time.Now()
	LAST_SCHEDULE_CHECK = &t

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

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return body, err
	}

	return body, err
}

func initializeSession(uri string) (*http.Client, error) {

	log.Println("Initializing session...")

	var err error
	jar, _ := cookiejar.New(nil)

	var tr *http.Transport
	if PROXY_HOST != "" {
		log.Printf("Using proxy %s", PROXY_HOST)
		tr = &http.Transport{
			Proxy: http.ProxyURL(&url.URL{
				Scheme: "http",
				Host:   PROXY_HOST,
				User: url.UserPassword(
					PROXY_USERNAME,
					PROXY_PASSWORD,
				),
			}),
		}
	}
	client := &http.Client{
		Jar:       jar,
		Transport: tr,
		Timeout:   5 * time.Second,
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
	// set timeout to a greater value for actual post requests
	client.Timeout = 30 * time.Second

	return client, err

}

func htmlNodeToString(n *html.Node) string {
	var str string
	var buf bytes.Buffer
	w := io.Writer(&buf)
	html.Render(w, n)
	str = buf.String()
	return strings.ReplaceAll(html.UnescapeString(strings.TrimSpace(str)), "\n", " <LF> ")
}

func htmlNodeGetChildrenText(n *html.Node) string {
	var str string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			str += c.Data
		}
	}
	return html.UnescapeString(strings.TrimSpace(str))
}

func parseHTMLFindErrorList(body []byte) (string, error) {
	var err error
	var str string
	// Recursively parse the HTML tree
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "ul" {
			for _, a := range n.Attr {
				if a.Key == "class" && a.Val == "error" {
					str = htmlNodeToString(n)
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

func parseHTMLFindForm(body []byte) (string, error) {
	var err error
	var str string
	// Recursively parse the HTML tree
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "form" {
			for _, a := range n.Attr {
				if a.Key == "name" && a.Val == "create" {
					str = htmlNodeToString(n)
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

func logToOwner(text string) {
	msg := tgbotapi.NewMessage(
		TELEGRAM_BOT_OWNER_CHAT_ID,
		text,
	)
	_, err := BOT.Send(msg)
	if err != nil {
		log.Printf("Got error while sending message to subscriber %d %s", TELEGRAM_BOT_OWNER_CHAT_ID, err.Error())
	}
}

func main() {

	err := initializeTelegramBot()
	if err != nil {
		log.Fatal(err)
	}

	var client *http.Client
	var nbInits int
	for {
		client, err = initializeSession(ENDPOINT)
		if err != nil {
			nbInits++
			log.Printf("Error initializing session: %s. Tentative %d", err, nbInits)
			continue
		}
		break
	}

	t0 := time.Now()

	for {
		form := url.Values{
			// "planning":   {planningID},
			"condition":  {"on"},
			"nextButton": {"Effectuer+une+demande+de+rendez-vous"},
		}
		body, err := doHTTPPostRequest(ENDPOINT, form, client)
		if err != nil {
			// logToOwner(fmt.Sprintf("Got error while doing HTTP POST request: %s. Exiting..", err.Error()))
			log.Fatal(err)
		}
		str, err := parseHTMLFindErrorList(body)
		if err != nil {
			logToOwner(fmt.Sprintf("Got error while parsing HTML for error list: %s. Exiting..", err.Error()))
			log.Fatal(err)
		}
		log.Printf("Error list result: %s\n", str)

		if str == "" {
			str, err = parseHTMLFindForm(body)
			if err != nil {
				logToOwner(fmt.Sprintf("Got error while parsing HTML for create form: %s. Exiting..", err.Error()))
				log.Fatal(err)
			}
			log.Printf("Form result: %s\n", str)
		}

		t := time.Now()
		LAST_SCHEDULE_CHECK = &t

		if str == "" {
			log.Printf("Got empty result")
			logToOwner("Got empty result")
			time.Sleep(15 * time.Second)
			continue
		}

		if !strings.Contains(str, "Il n'y a pas calendrier disponible pour effectuer une demande de rendez-vous") &&
			!strings.Contains(str, "Il n'existe plus de plage horaire libre") {
			log.Printf("An appointment might be available: %s", str)

			saveFile(
				filepath.Join("results",
					fmt.Sprintf("result-appointment-might-be-available-%d.html", time.Now().UnixMilli()),
				),
				body,
			)

			subscriberIDs, err := loadSubscrierIDs()
			if err != nil {
				log.Printf("Got error while loading subscriber IDs %s", err.Error())
			}
			for _, subscriberID := range subscriberIDs {
				msg := tgbotapi.NewMessage(
					subscriberID,
					"An appointment might be available!",
				)
				_, err = BOT.Send(msg)
				if err != nil {
					log.Printf("Got error while sending message to subscriber %d %s", subscriberID, err.Error())
				}
			}
		}
		log.Println("Appointment not available..")
		time.Sleep(15 * time.Second)

		if time.Since(t0) > 10*time.Minute {
			os.Exit(0)
		}
	}
}
