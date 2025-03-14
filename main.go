package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/flytam/filenamify"
	_ "github.com/joho/godotenv/autoload"
	"github.com/tidwall/gjson"
)

var config = &Configs{
	MazeBaseURL: "https://api.tvmaze.com/shows?page=",
	UmbBaseURL:  "https://api.rainbowsrock.net/",
}

var UMB_PROJ_ALIAS = os.Getenv("UMB_PROJECT_ALIAS")
var UMB_API_KEY = os.Getenv("API_KEY")

const WORKER_COUNT = 5 // Number of concurrent page uploads. 5 seems to be just hitting the rate limit
const PAGE_SIZE = 250
const TOTAL_PAGES = 332 // Real number is 332
const LANGUAGE = "en-US"

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

	fmt.Println("Beginning umbraco download...")
	allUmbShows := getAllUmbShows(totalUmbShows)
	if allUmbShows == nil {
		fmt.Println("Unable to fetch umb shows")
		return
	}
	println("Total Umbraco shows fetched: ", len(allUmbShows))

	// DELETION CODE COMMENT OUT TO DELETE ALL DATA FROM UMBRACO
	// deleteAll(allUmbShows) // If duplicates are in the map, they wont be deleted and will need a second pass
	// return

	// make Umbraco entries into hashmap based on ID
	// Start fetching and uploading Maze movies.
	// 		If a movie exists in memory, and the data is not empty, skip it.
	//		If a movie exists in memory, and it has empty values, update it if possible
	//		If a movie doesn't exist, create and upload it
	fmt.Println("Beginning upload...")
	defer timeTrack(time.Now(), "Total time to upload")
	var wg sync.WaitGroup
	pageChan := make(chan int, WORKER_COUNT)

	// Start worker goroutines
	for i := 0; i < WORKER_COUNT; i++ {
		go func() {
			for page := range pageChan {
				processPage(page, allUmbShows, &wg)
			}
		}()
	}

	// Send pages to workers
	for page := 0; page <= TOTAL_PAGES; page++ {
		wg.Add(1)
		pageChan <- page
	}

	// Close channel when all pages are sent
	close(pageChan)

	// Wait for all workers to finish
	wg.Wait()
}

func deleteAll(allUmbShows map[int]Show) {
	defer timeTrack(time.Now(), "Deletion of all shows and images")
	client := &http.Client{Timeout: 60 * time.Second}
	total := len(allUmbShows)
	count := 0
	for _, show := range allUmbShows {
		fmt.Printf("Deleted %d of %d         \r", count, total)
		count++

		url := fmt.Sprintf("%scontent/%s", config.UmbBaseURL, show.UmbId)
		req, err := http.NewRequest("DELETE", url, nil)
		if err != nil {
			fmt.Println("Error creating request:", err)
			continue
		}
		setAuthHeader(req)

		resp, err := client.Do(req)
		if err != nil {
			fmt.Println("Error sending request:", err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			fmt.Printf("Failed to delete show %s, status code: %d\n", show.UmbId, resp.StatusCode)
		}

		if show.Image == "" {
			continue
		}
		url = fmt.Sprintf("%smedia/%s", config.UmbBaseURL, show.Image)
		req, err = http.NewRequest("DELETE", url, nil)
		if err != nil {
			fmt.Println("Error creating request:", err)
			continue
		}
		setAuthHeader(req)

		resp, err = client.Do(req)
		if err != nil {
			fmt.Println("Error sending request:", err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			fmt.Printf("Failed to delete image %s, status code: %d\n", show.UmbId, resp.StatusCode)
		}
	}
	req, err := http.NewRequest("GET", config.UmbBaseURL+"media", nil)
	if err != nil {
		fmt.Println("Error creating request:", err)
		return
	}
	setAuthHeader(req)

	// Send request
	fmt.Println("Downloading all image ID's to delete")
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		fmt.Printf("Error sending request. Status: %d\nError: %v", resp.StatusCode, err)
		return
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading response:", err)
		return
	}

	images := gjson.Get(string(body), "_embedded.media")
	if !images.Exists() {
		return
	}
	images.ForEach(func(i, umbShow gjson.Result) bool {

		umbId := umbShow.Get("_id")
		url := fmt.Sprintf("%smedia/%s", config.UmbBaseURL, umbId)
		req, err := http.NewRequest("DELETE", url, nil)
		if err != nil {
			fmt.Println("Error creating request:", err)
			return true
		}
		setAuthHeader(req)

		resp, err := client.Do(req)
		if err != nil {
			fmt.Println("Error sending request:", err)
			return true
		}
		fmt.Println("Deleted img: ", umbId)
		resp.Body.Close()
		return true // keep iterating
	})
}

func processPage(page int, allUmbShows map[int]Show, wg *sync.WaitGroup) int {
	defer timeTrack(time.Now(), fmt.Sprintf("Page %3d completed upload", page))
	defer wg.Done()
	mazePage, err := getMazePage(config.MazeBaseURL + strconv.Itoa(page))
	if err != nil {
		println(err)
		return 1
	}
	count := 1
	for _, mazeShow := range mazePage {
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
				}
				fmt.Printf("Page: %6d\tCount: %6d\tID: %6d\r", page, count, mazeShow.Id)
			} else {
				fmt.Printf("Page: %6d\tCount: %6d\tID: %6d\r", page, count, mazeShow.Id)
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

			err = retry(8, 200*time.Millisecond, 10*time.Second, func() error {
				return sendUmbShow("POST", mazeShow)
			})
			if err != nil {
				fmt.Println("Error when creating umbraco show", err)
			}
			//fmt.Printf("Page: %6d\tCount: %6d\tID: %6d\r", page, count, mazeShow.Id)
		}
		count++
	}
	return 0
}

func retry(attempts int, initialDelay time.Duration, maxDelay time.Duration, fn func() error) error {
	var err error = nil
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
	var err error = nil
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
		fmt.Println("Error downloading shows:", err)
		return shows, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return shows, fmt.Errorf("end of shows API reached")
	}
	// Check if the request was successful
	if resp.StatusCode != http.StatusOK {
		fmt.Println("Failed to download shows, status:", resp.Status)
		return shows, err
	}

	// Read the show data
	showsData, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading shows data:", err)
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
		if resp != nil {
			resp.Body.Close()
		}
		if strings.Contains(err.Error(), "unsupported protocol scheme") {
			return "", nil // Skip retry
		}
		return "", err
	}

	if resp == nil {
		fmt.Println("Failed to get response image")
		return "", err
	}

	// Check if the request was successful
	if resp.StatusCode != http.StatusOK {
		fmt.Println("Failed to download image, status:", resp.Status)
		resp.Body.Close()
		return "", err
	}

	// Read the image data
	imageData, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading image data:", err)
		resp.Body.Close()
		return "", err
	}
	resp.Body.Close()

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
	if err != nil || resp == nil {
		fmt.Println("Error sending request:", err)
		return "", err
	}
	defer resp.Body.Close()
	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 201 {
		//fmt.Println("Error reading umb image response:", err)
		return "", err
	}

	return gjson.Get(string(respBody), "_id").String(), nil
}

func sendUmbShow(requestType string, show Show) error {
	// TODO: Generate Genres json string
	genreJson, err := genreFormatter(show.Genres)
	if err != nil {
		genreJson = ""
	}
	imgJson := ""
	if show.Image != "" {
		imgJson = fmt.Sprintf(`{
					"mediaKey": "%s"
				}`, show.Image)
	}
	// Create JSON string with fmt.Sprintf
	jsonData := fmt.Sprintf(`{
		"parentId": "%s",
		"sortOrder": 0,
		"contentTypeAlias": "tVShow",
		"name": {
			"%s": "%s"
		},
		"genres": {
			"$invariant": {
				%s
				"settingsData": []
			}
		},
		"showId": {
			"$invariant": %d
		},
		"showSummary": {
			"%s": "%s"
		},
		"showImage": {
			"$invariant": [
				%s
			]
		}
	}`, config.UmbRootItemId, LANGUAGE, strings.ReplaceAll(show.Name, "\"", "\\\""), genreJson, show.Id, LANGUAGE, strings.ReplaceAll(show.Summary, "\"", "\\\""), imgJson)
	url := ""
	if requestType == "POST" {
		url = config.UmbBaseURL + "content"
	} else {
		url = config.UmbBaseURL + "content/" + show.UmbId
	}
	req, err := http.NewRequest(requestType, url, bytes.NewBuffer([]byte(jsonData)))
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
	if err != nil {
		fmt.Printf("Error sending request. ID: %d. Error: %v\n", show.Id, err)
		return err
	}

	// Ensure response is valid before accessing StatusCode
	if resp == nil {
		fmt.Printf("Error: response is nil for ID: %d skipping...\n", show.Id)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		fmt.Printf("Server error. ID: %d. Status: %d\n", show.Id, resp.StatusCode)
		return fmt.Errorf("status error: %d", resp.StatusCode)
	}
	return nil
}

func genreFormatter(genres []Genre) (string, error) {
	if len(genres) == 0 {
		return "", nil
	}

	var layout Layout
	var contentData []ContentData
	contentTypeKey := "8a2cd752-ace4-433c-a6ce-f80a70405407"

	for _, genre := range genres {
		// Generate unique UDI for each genre
		udi := fmt.Sprintf("umb://element/%s", generateCustomUUID()) // Function to generate UDI
		layout.UmbracoBlockList = append(layout.UmbracoBlockList, ContentUdi{ContentUdi: udi})
		contentData = append(contentData, ContentData{
			ContentTypeKey: contentTypeKey,
			Udi:            udi,
			IndexNumber:    fmt.Sprintf("%d", genre.Index),
			Title:          genre.Title,
		})
	}

	// Create the full JSON structure
	genreJson := JSONGenres{
		Layout:      layout,
		ContentData: contentData,
	}

	// Marshal to JSON
	jsonBytes, err := json.MarshalIndent(genreJson, "", "	")
	if err != nil {
		return "", err
	}
	// Convert to string and remove outer curly braces
	jsonString := string(jsonBytes)
	jsonString = strings.TrimPrefix(jsonString, "{")
	jsonString = strings.TrimSuffix(jsonString, "}")
	jsonString += ","

	return jsonString, nil
}

// generateCustomUUID generates a 32-character random string with lowercase letters and digits.
func generateCustomUUID() string {
	newUUID, err := uuid.NewRandom() // Generate a random UUID
	if err != nil {
		panic(err) // Handle errors properly in production
	}
	return strings.ReplaceAll(newUUID.String(), "-", "") // Remove dashes

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

		UmbId := umbShow.Get("_id")
		show.UmbId = UmbId.String()

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
	req.Header.Set("umb-project-alias", UMB_PROJ_ALIAS)
	req.Header.Set("Api-Key", UMB_API_KEY)
}

type Configs struct {
	UmbRootItemId  string `json:"root_id,omitempty"`
	UmbRootItemURL string `json:"root_url,omitempty"`
	MazeBaseURL    string `json:"maze_base_url,omitempty"`
	UmbBaseURL     string `json:"umb_base_url,omitempty"`
}

type Show struct {
	UmbId   string  `json:"_id,omitempty"`         // Found in umbraco ~content._id
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

// Used for Umbraco API JSON formatting
type Layout struct {
	UmbracoBlockList []ContentUdi `json:"Umbraco.BlockList"`
}

// Used for Umbraco API JSON formatting
type ContentUdi struct {
	ContentUdi string `json:"contentUdi"`
}

// Used for Umbraco API JSON formatting
type ContentData struct {
	ContentTypeKey string `json:"contentTypeKey"`
	Udi            string `json:"udi"`
	IndexNumber    string `json:"indexNumber"`
	Title          string `json:"title"`
}

// Used for Umbraco API JSON formatting
type JSONGenres struct {
	Layout      Layout        `json:"layout"`
	ContentData []ContentData `json:"contentData"`
}
