package main

import (
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

	config.RootURL = rootUrl
	_urlSplit := strings.Split(rootUrl, "/")
	config.RootId = _urlSplit[len(_urlSplit)-1]
	fmt.Println("Root URL fetched: ", config.RootURL)

	// https: //api.rainbowsrock.net/content/cf824770-d453-4a78-b572-3874e367b878/children
	// Create a new GET request
	// req, err := http.NewRequest("GET", url, nil)
	// if err != nil {
	// 	fmt.Println("Error creating request:", err)
	// 	return
	// }

	// // Set headers
	// req.Header.Set("umb-project-alias", "your-project-alias") // Replace with actual alias
	// req.Header.Set("Api-Key", "your-api-key")                 // Replace with actual API key

	// // Send request
	// resp, err := client.Do(req)
	// if err != nil {
	// 	fmt.Println("Error sending request:", err)
	// 	return
	// }
	// page := 0
	// // Remember to wrap all in while true and check for status 200 to see if there are more pages on the api
	// resp, err := http.Get(baseUrl + strconv.Itoa(page))
	// if err != nil {
	// 	fmt.Printf("error making http request: %s\n", err)
	// 	os.Exit(1)
	// }
	// defer resp.Body.Close()

	// Decode JSON response
	// data := &[]TVMazeShow{}
	// err = json.NewDecoder(resp.Body).Decode(data)
	// if err != nil {
	// 	fmt.Println("Error decoding JSON:", err)
	// 	return
	// }

	// Get a list of all current shows in Umbraco https://api.rainbowsrock.net/content/cf824770-d453-4a78-b572-3874e367b878/children
	// "_embedded:content:_links:children:href"

	// for _, show := range *data {
	// 	fmt.Printf("%+v\n", show)

	// 	// For each show, download it from Umbraco, check if it matches the Maze version. https://docs.umbraco.com/umbraco-heartcore/api-documentation/content-management/content#get-by-id
	// 	// If it does not exist in Umbraco, create/upload it https://docs.umbraco.com/umbraco-heartcore/api-documentation/content-management/content#create-content-with-files
	// 	// If it does exist, check if the data is the same
	// 	// If the data is the same, skip this
	// 	// If the data is not the same, update it https://docs.umbraco.com/umbraco-heartcore/api-documentation/content-management/content#update-content

	// 	return
	// }

	// Publish new content https://docs.umbraco.com/umbraco-heartcore/api-documentation/content-management/content#publish-content
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
	RootId          string `json:"root_id,omitempty"`
	RootURL         string `json:"root_url,omitempty"`
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
