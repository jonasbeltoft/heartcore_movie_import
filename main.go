package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
)

var baseUrl = "https://api.tvmaze.com/shows?page="

func main() {
	page := 0

	response, err := http.Get(baseUrl + strconv.Itoa(page))
	if err != nil {
		fmt.Printf("error making http request: %s\n", err)
		os.Exit(1)
	}
	fmt.Println(response.ContentLength)
}

type TVMazeShow struct {
	Id    int    `json:"id"`
	Name  string `json:"name,omitempty"`
	Image string `json:"image,omitempty"`
}

type Image struct {
	Medium   string `json:"medium,omitempty"`
	Original string `json:"original,omitempty"`
}
