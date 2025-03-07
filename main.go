package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flytam/filenamify"
	_ "github.com/joho/godotenv/autoload"
	"github.com/tidwall/gjson"
)

var config = &Configs{
	MazeBaseURL:     "https://api.tvmaze.com/shows?page=",
	UmbBaseURL:      "https://api.rainbowsrock.net/",
	UmbProjectAlias: os.Getenv("UMB_PROJECT_ALIAS"),
	UmbApiKey:       os.Getenv("API_KEY"),
}

var PAGE_SIZE = 250
var LANGUAGE = "en-US"

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

	// https://docs.umbraco.com/umbraco-heartcore/api-documentation/content-management/content
	// Get total items, iterate over all of them, and download to memory.

	totalUmbShows := getUmbShowCount()

	allUmbShows := getAllUmbShows(totalUmbShows)
	if allUmbShows == nil {
		fmt.Println("Unable to fetch umb shows")
		return
	}

	// make Umbraco entries into hashmap based on ID
	// Start fetching and uploading Maze movies.
	// 		If a movie exists in memory, and the data is not empty, skip it.
	//		If a movie exists in memory, and it has empty values, update it if possible
	//		If a movie doesn't exist, create and upload it
	fmt.Println("Beginning upload...")
	defer timeTrack(time.Now(), "Total time to upload")
	page := 0
	for {

		mazePage, err := getMazePage(config.MazeBaseURL + strconv.Itoa(page))
		if err != nil {
			println("error when fething show", err)
			return
		}

		for _, mazeShow := range mazePage {
			fmt.Printf("ID: %d\r", mazeShow.Id)
			// If it already is in umbraco
			if umbShow, exists := allUmbShows[mazeShow.Id]; exists {
				// If no image is attached, upload image
				doUpload := false
				if umbShow.Image == "" {
					key, err := retryImage(8, 200*time.Millisecond, 10*time.Second, func() (string, error) {
						return createUmbImage(mazeShow.Name, mazeShow.Image)
					})
					if err != nil {
						println("error when uploading image", err)
						umbShow.Image = ""
					} else {
						umbShow.Image = key
						doUpload = true
					}
				}
				// TODO Genres
				if umbShow.Name != mazeShow.Name || umbShow.Summary != mazeShow.Summary {
					umbShow.Name = mazeShow.Name
					umbShow.Summary = mazeShow.Summary
					doUpload = true
				}

				if doUpload {
					err := retry(8, 200*time.Millisecond, 10*time.Second, func() error {
						return sendUmbShow("PUT", umbShow)
					})
					if err != nil {
						fmt.Println("Error when creating umbraco show", err)
						continue
					}
				}
			} else {
				key, err := retryImage(8, 200*time.Millisecond, 10*time.Second, func() (string, error) {
					return createUmbImage(mazeShow.Name, mazeShow.Image)
				})
				if err != nil {
					println("error when uploading image", err)
					mazeShow.Image = ""
				} else {
					mazeShow.Image = key
				}

				// TODO Genres
				err = retry(8, 200*time.Millisecond, 10*time.Second, func() error {
					return sendUmbShow("POST", mazeShow)
				})
				if err != nil {
					fmt.Println("Error when creating umbraco show", err)
					continue
				}
			}

		}

		break // Test one page e.g. 250 shows
	}
	// uploadBatch(*mazeShowsPaged, "UMBRACO UPLOAD URL")
}

func retry(attempts int, initialDelay time.Duration, maxDelay time.Duration, fn func() error) error {
	var err error
	delay := initialDelay

	for i := 0; i < attempts; i++ {
		err = fn()
		if err == nil {
			return nil // Success, exit early
		}

		fmt.Printf("Attempt %d failed: %v\n", i+1, err)

		// Wait before retrying
		time.Sleep(delay)

		// Increase delay exponentially, capping at maxDelay
		delay = time.Duration(math.Min(float64(delay*2), float64(maxDelay)))
	}

	return fmt.Errorf("all %d retry attempts failed: %w", attempts, err)
}
func retryImage(attempts int, initialDelay time.Duration, maxDelay time.Duration, fn func() (string, error)) (string, error) {
	var err error
	delay := initialDelay

	for i := 0; i < attempts; i++ {
		key, err := fn()
		if err == nil {
			return key, nil // Success, exit early
		}

		fmt.Printf("Attempt %d failed: %v\n", i+1, err)

		// Wait before retrying
		time.Sleep(delay)

		// Increase delay exponentially, capping at maxDelay
		delay = time.Duration(math.Min(float64(delay*2), float64(maxDelay)))
	}

	return "", fmt.Errorf("all %d retry attempts failed: %w", attempts, err)
}

func getAllUmbShows(totalUmbShows int) map[int]Show {
	defer timeTrack(time.Now(), "Download and parse all umb shows")
	allUmbShows := make(map[int]Show)
	// Pages in umbraco are 1 indexed, so it starts at one, and +1 is to compensate for rounding down
	for i := 1; i <= (totalUmbShows/PAGE_SIZE)+1; i++ {
		// paging syntax: BaseURL/children?page=1&pageSize=10
		url := fmt.Sprintf("%s/children?page=%d&pageSize=%d", config.UmbRootItemURL, i, PAGE_SIZE)
		shows, err := getUmbShowPage(url)
		if err != nil {
			fmt.Println("Failed to fetch umbraco page. ", err)
			return nil
		}

		for _, show := range shows {
			allUmbShows[show.Id] = show
		}
	}
	return allUmbShows
}

// Temp function
func getMazePage(url string) ([]Show, error) {
	defer timeTrack(time.Now(), "Downloading a page")

	shows := []Show{}
	// Fetch the show from the URL
	resp, err := http.Get(url)
	if err != nil {
		fmt.Println("Error downloading image:", err)
		return shows, err
	}
	defer resp.Body.Close()

	// Check if the request was successful
	if resp.StatusCode != http.StatusOK {
		fmt.Println("Failed to download image, status:", resp.Status)
		return shows, err
	}

	// Read the show data
	showsData, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading image data:", err)
		return shows, err
	}
	showsGJson := gjson.Parse(string(showsData))
	if showsGJson.Exists() {
		showsGJson.ForEach(func(i, show gjson.Result) bool {
			_show := Show{}
			_show.Id = int(show.Get("id").Int())
			_show.Name = show.Get("name").String()
			_show.Summary = show.Get("summary").String()
			_show.Image = show.Get("image.medium").String()
			genres := show.Get("genres")
			if genres.Exists() {
				newGenres := []Genre{}
				for i, val := range genres.Array() {
					genre := Genre{
						Index: i,
						Title: val.String(),
					}
					newGenres = append(newGenres, genre)
				}
				_show.Genres = newGenres
			}
			shows = append(shows, _show)
			return true // Keep iterating
		})
	}
	return shows, nil
}

// Returns the mediaKey of this new media image
func createUmbImage(imgName string, imgUrl string) (string, error) {

	imgName, err := filenamify.Filenamify(imgName, filenamify.Options{
		Replacement: "_",
	})
	imgName += ".jpg"
	if err != nil {
		fmt.Println("Failed to convert image name to file name", err)
		return "", err
	}
	// Define the metadata as a struct
	metadata := map[string]interface{}{
		"mediaTypeAlias": "Image",
		"name":           imgName,
		"umbracoFile": map[string]string{
			"src": imgName,
		},
	}

	// Convert metadata to JSON
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		fmt.Println("Error encoding JSON:", err)
		return "", err
	}

	// Fetch the image from the URL
	resp, err := http.Get(imgUrl)
	if err != nil {
		fmt.Println("Error downloading image:", err)
		return "", err
	}
	defer resp.Body.Close()

	// Check if the request was successful
	if resp.StatusCode != http.StatusOK {
		fmt.Println("Failed to download image, status:", resp.Status)
		return "", err
	}

	// Read the image data
	imageData, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading image data:", err)
		return "", err
	}

	// Create a buffer and a multipart writer
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add the JSON metadata as a form field
	metadataPart, err := writer.CreateFormField("content")
	if err != nil {
		fmt.Println("Error creating metadata part:", err)
		return "", err
	}
	_, err = metadataPart.Write(metadataJSON)
	if err != nil {
		fmt.Println("Error writing metadata:", err)
		return "", err
	}

	// Create the file field in the multipart form
	filePart, err := writer.CreateFormFile("umbracoFile", imgName)
	if err != nil {
		fmt.Println("Error creating file part:", err)
		return "", err
	}

	// Write the image data to the file part
	_, err = filePart.Write(imageData)
	if err != nil {
		fmt.Println("Error writing image data:", err)
		return "", err
	}

	// Close the writer to finalize the multipart body
	writer.Close()

	// Create the request
	req, err := http.NewRequest("POST", config.UmbBaseURL+"media", body)
	if err != nil {
		fmt.Println("Error creating request:", err)
		return "", err
	}

	// Set headers
	setAuthHeader(req)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send the request
	client := &http.Client{}
	resp, err = client.Do(req)
	if err != nil {
		fmt.Println("Error sending request:", err)
		return "", err
	}
	defer resp.Body.Close()
	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 201 {
		fmt.Println("Error reading response:", err)
		return "", err
	}

	return gjson.Get(string(respBody), "_id").String(), nil
}

func sendUmbShow(requestType string, show Show) error {
	// Create JSON string with fmt.Sprintf

	// TODO: Handle quotations in summary and maybe name and other text. It causes errors

	jsonData := fmt.Sprintf(`{
		"parentId": "%s",
		"sortOrder": 0,
		"contentTypeAlias": "tVShow",
		"name": {
			"%s": "%s"
		},
		"showId": {
			"$invariant": %d
		},
		"showSummary": {
			"%s": "%s"
		},
		"showImage": {
			"$invariant": [
				{
					"mediaKey": "%s"
				}
			]
		}
	}`, config.UmbRootItemId, LANGUAGE, strings.ReplaceAll(show.Name, "\"", "\\\""), show.Id, LANGUAGE, strings.ReplaceAll(show.Summary, "\"", "\\\""), show.Image)

	req, err := http.NewRequest(requestType, config.UmbBaseURL+"content", bytes.NewBuffer([]byte(jsonData)))
	if err != nil {
		fmt.Println("Error creating request:", err)
		return err
	}
	setAuthHeader(req)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Transfer-Encoding", "chunked")
	req.Header.Set("Connection", "keep-alive")

	// Send request
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil || (requestType == "POST" && resp.StatusCode != 201) || (requestType == "PUT" && resp.StatusCode != 200) {
		fmt.Printf("Error sending request. Status: %d\tError: %v\n", resp.StatusCode, err)
		if err == nil {
			err = fmt.Errorf("status error")
		}
		return err
	}
	defer resp.Body.Close()
	return nil
}

// NOT FINISHED
// uploadBatch uploads a batch of shows and returns an error if a fatal issue occurs
func uploadBatch(shows []Show, apiURL string) error {
	payload, err := json.Marshal(shows)
	if err != nil {
		return fmt.Errorf("failed to serialize batch: %w", err)
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	var resp *http.Response
	retries := 3

	for i := 0; i < retries; i++ {
		resp, err = client.Do(req)
		if err != nil {
			// Check if error is a timeout
			if errors.Is(err, http.ErrHandlerTimeout) {
				fmt.Printf("Timeout error, retrying... (%d/%d)\n", i+1, retries)
				time.Sleep(time.Duration(2<<i) * time.Second) // Exponential backoff
				continue
			}
			return fmt.Errorf("fatal error: %w", err)
		}
		defer resp.Body.Close()

		// Handle response codes
		if resp.StatusCode == http.StatusOK {
			return nil
		} else if resp.StatusCode == http.StatusTooManyRequests {
			fmt.Println("Rate limit hit, retrying...")
			time.Sleep(time.Duration(2<<i) * time.Second)
			continue
		} else {
			fmt.Printf("Skipping failed upload (status %d)\n", resp.StatusCode)
			return nil
		}
	}

	return fmt.Errorf("batch upload failed after retries")
}

func getUmbShowPage(url string) ([]Show, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		fmt.Println("Error creating request:", err)
		return nil, err
	}
	setAuthHeader(req)

	// Send request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		fmt.Printf("Error sending request. Status: %d\nError: %v", resp.StatusCode, err)
		return nil, err
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading response:", err)
		return nil, err
	}

	shows := gjson.Get(string(body), "_embedded.content")
	result := []Show{}
	if !shows.Exists() {
		return result, nil
	}
	shows.ForEach(func(i, umbShow gjson.Result) bool {
		show := Show{}

		id := umbShow.Get("showId.$invariant")
		if id.Exists() {
			_id := id.String()
			if _id != "" {
				num, err := strconv.Atoi(_id)
				if err == nil {
					show.Id = num
				}
			}
		}
		name := umbShow.Get(fmt.Sprintf("name.%s", LANGUAGE))
		if name.Exists() {
			if name.String() != "" {
				show.Name = name.String()
			}
		}
		genres := umbShow.Get("genres.$invariant.contentData.#.title")
		if genres.Exists() {
			newGenres := []Genre{}
			for i, val := range genres.Array() {
				genre := Genre{
					Index: i,
					Title: val.String(),
				}
				newGenres = append(newGenres, genre)
			}
			show.Genres = newGenres
		}
		summary := umbShow.Get(fmt.Sprintf("showSummary.%s.markup", LANGUAGE))
		if summary.Exists() {
			show.Summary = summary.String()
		}

		image := umbShow.Get("showImage.$invariant.0.mediaKey")
		if image.Exists() {
			show.Image = image.String()
		}

		result = append(result, show)

		return true // keep iterating
	})

	return result, nil
}

func getUmbShowCount() int {
	req, err := http.NewRequest("GET", config.UmbRootItemURL+"/children", nil)
	if err != nil {
		fmt.Println("Error creating request:", err)
		os.Exit(1)
	}
	setAuthHeader(req)

	// Send request
	client := &http.Client{}
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
	return int(count)
}

func getRootIdUrl(client *http.Client) string {
	req, err := http.NewRequest("GET", config.UmbBaseURL+"content", nil)
	if err != nil {
		fmt.Println("Error getting base URL:", err)
		os.Exit(1)
	}
	setAuthHeader(req)

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

func setAuthHeader(req *http.Request) {
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

type Show struct {
	Id      int     `json:"showId,omitempty"`      // Found in umbraco: ~content.showId.$invariant   found in TVMaze: id
	Name    string  `json:"name,omitempty"`        // Found in umbraco: ~content.name.$invariant   found in TVMaze: name
	Genres  []Genre `json:"genres,omitempty"`      // Found in TVMaze content body as array of strings (titles only): genres
	Summary string  `json:"showSummary,omitempty"` // Found in umbraco: ~content.showSummary.en-US.markup	found in TVMaze: summary
	Image   string  `json:"showImage,omitempty"`   // Found in umbraco (is a UID): ~content.showImage.$invariant.[].mediaKey   found in TVMaze (link): image.original
}

type Genre struct {
	Index int    `json:"indexNumber,omitempty"` // Found in umbraco content body: ~content.genres.$invariant.contentData.indexNumber
	Title string `json:"title,omitempty"`       // Found in umbraco content body: ~content.genres.$invariant.contentData.title
}

func timeTrack(start time.Time, name string) {
	elapsed := time.Since(start)
	log.Printf("%s took %s", name, elapsed)
}
