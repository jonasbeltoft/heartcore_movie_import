package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	_ "github.com/joho/godotenv/autoload"
	"github.com/tidwall/gjson"
)

var config = Configs{
	MazeBaseURL:     "https://api.tvmaze.com/shows?page=",
	UmbBaseURL:      "https://api.rainbowsrock.net/",
	UmbProjectAlias: os.Getenv("UMB_PROJECT_ALIAS"),
	UmbApiKey:       os.Getenv("API_KEY"),
}

func main() {

	// Create an HTTP client
	client := &http.Client{}

	rootUrl := getRootIdUrl(client)
	if rootUrl == "" {
		fmt.Println("Root URL is empty")
		return
	}

	config.UmbRootItemURL = rootUrl
	_urlSplit := strings.Split(rootUrl, "/")
	config.UmbRootItemId = _urlSplit[len(_urlSplit)-1]
	fmt.Println("Root URL fetched: ", config.UmbRootItemURL)

	// https://docs.umbraco.com/umbraco-heartcore/api-documentation/content-management/content
	// Get total items, iterate over all of them, and download to memory.

	totalUmbShows := getUmbShowCount(client)
	PAGE_SIZE := 100
	allUmbShows := &[]UmbShow{}

	// Pages in umbraco are 1 indexed, so it starts at one, and +1 is to compensate for rounding down and
	for i := 1; i <= int(int(totalUmbShows)/PAGE_SIZE)+1; i++ {
		// paging syntax: BaseURL/children?page=1&pageSize=10
		url := fmt.Sprintf("%s/children?page=%d&pageSize=%d", config.UmbRootItemURL, i, PAGE_SIZE)
		shows := getUmbShowPage(client, url)
		*allUmbShows = append(*allUmbShows, *shows...)
	}
	str, err := json.MarshalIndent(allUmbShows, "", "  ")
	if err != nil {
		return
	}
	fmt.Println(string(str))

	// Start fetching and uploading Maze movies.
	// 		If a movie exists in memory, and the data is not empty, skip it.
	//		If a movie exists in memory, and it's empty, update it
	//		If a movie doesn't exist, create and upload it

}

func getUmbShowPage(client *http.Client, url string) *[]UmbShow {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		fmt.Println("Error creating request:", err)
		os.Exit(1)
	}
	setHeader(req)

	// Send request
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		fmt.Printf("Error sending request. Status: %d\nError: %v", resp.StatusCode, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading response:", err)
		os.Exit(1)
	}

	showsJson := gjson.Get(string(body), "_embedded.content").String()
	result := &[]UmbShow{}

	err = json.Unmarshal([]byte(showsJson), result)
	if err != nil {
		fmt.Println("Error decoding JSON:", err)
		os.Exit(1)
	}

	return result
}

func getUmbShowCount(client *http.Client) int64 {
	req, err := http.NewRequest("GET", config.UmbRootItemURL+"/children", nil)
	if err != nil {
		fmt.Println("Error creating request:", err)
		os.Exit(1)
	}
	setHeader(req)

	// Send request
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		fmt.Printf("Error sending request. Status: %d\nError: %v", resp.StatusCode, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading response:", err)
		os.Exit(1)
	}

	count := gjson.Get(string(body), "_totalItems").Int()
	return count
}

func getRootIdUrl(client *http.Client) string {
	req, err := http.NewRequest("GET", config.UmbBaseURL+"content", nil)
	if err != nil {
		fmt.Println("Error getting base URL:", err)
		os.Exit(1)
	}
	setHeader(req)

	// Send request
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		fmt.Printf("Error sending request. Status: %d\nError: %v", resp.StatusCode, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading response:", err)
		os.Exit(1)
	}

	href := gjson.Get(string(body), "_links.content.1.href").String()
	if href == "" {
		fmt.Println("Error: Could not find the desired link in JSON")
		os.Exit(1)
	}
	return href
}

func setHeader(req *http.Request) {
	req.Header.Set("umb-project-alias", config.UmbProjectAlias)
	req.Header.Set("Api-Key", config.UmbApiKey)
}

type Configs struct {
	UmbRootItemId   string `json:"root_id,omitempty"`
	UmbRootItemURL  string `json:"root_url,omitempty"`
	UmbProjectAlias string `json:"umb-project-alias,omitempty"`
	UmbApiKey       string `json:"Api-Key,omitempty"`
	MazeBaseURL     string `json:"maze_base_url,omitempty"`
	UmbBaseURL      string `json:"umb_base_url,omitempty"`
}

type TVMazeShow struct {
	Id    int    `json:"id"`
	Name  string `json:"name,omitempty"`
	Image Image  `json:"image,omitempty"`
}

type Image struct {
	Medium   string `json:"medium,omitempty"`
	Original string `json:"original,omitempty"`
}

type UmbShow struct {
	Genres      Genres      `json:"genres"`
	ShowID      Invariant   `json:"showId"`
	ShowSummary ShowSummary `json:"showSummary"`
	ShowImage   Invariant   `json:"showImage"`
}

// Genres struct contains the $invariant field
type Genres struct {
	Invariant interface{} `json:"$invariant,omitempty"`
}

// Invariant is used for fields like "showId" and "showImage"
type Invariant struct {
	Invariant interface{} `json:"$invariant"` // Can be string, array, or other types
}

// ShowSummary contains localized summaries
type ShowSummary struct {
	EnUS *LocaleContent `json:"en-US,omitempty"`
	DaDK *interface{}   `json:"da-DK,omitempty"` // Null value placeholder
}

// LocaleContent contains markup and blocks
type LocaleContent struct {
	Markup string `json:"markup"`
	Blocks Blocks `json:"blocks"`
}

// Blocks structure for showSummary
type Blocks struct {
	Layout       interface{}   `json:"layout"` // Null value placeholder
	ContentData  []interface{} `json:"contentData"`
	SettingsData []interface{} `json:"settingsData"`
}
