package main

import (
	"encoding/json"
	"fmt"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"sync"
)

var chartData *ChartData

func fetchAndParse(url string) (*html.Node, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP response code: %v", resp.StatusCode)
	}
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	node, err := html.Parse(strings.NewReader(string(data[:])))
	if err != nil {
		return nil, err
	}
	return node, nil
}

type nodeConditionFunc func(*html.Node) bool

func findDescendant(node *html.Node, conditionFunc nodeConditionFunc) *html.Node {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if conditionFunc(child) {
			return child
		}
		res := findDescendant(child, conditionFunc)
		if res != nil {
			return res
		}
	}
	return nil
}

func findFollowupSibling(node *html.Node) *html.Node {
	for sibling := node.NextSibling; sibling != nil; sibling = sibling.NextSibling {
		if node.DataAtom == sibling.DataAtom {
			return sibling
		}
	}
	return nil
}

func getAttribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func getInnerText(node *html.Node) string {
	if node.Type == html.TextNode {
		return node.Data
	}
	res := ""
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		res += getInnerText(child)
	}
	return res
}

type DeviceData struct {
	DeviceName    string
	TransmitBytes []int64
	ReceiveBytes  []int64
}

type ChartData struct {
	macAddressToDevice map[string]*DeviceData
	Devices            []*DeviceData
	FetchMilliseconds  []int64
	mu sync.Mutex
}

func fetchDevices(chartData *ChartData) error {
	doc, err := fetchAndParse("http://192.168.1.254/cgi-bin/devices.ha")
	chartData.mu.Lock()
	defer chartData.mu.Unlock()
	if err != nil {
		return err
	}
	table := findDescendant(doc, func(node *html.Node) bool {
		return node.DataAtom == atom.Table && getAttribute(node, "summary") == "This table displays info for each LAN-side device"
	})
	if table == nil {
		return nil
	}
	for tr := findDescendant(table, func(node *html.Node) bool {
		return node.DataAtom == atom.Tr
	}); tr != nil; tr = findFollowupSibling(tr) {
		th := findDescendant(tr, func(node *html.Node) bool {
			return node.DataAtom == atom.Th && getInnerText(node) == "MAC Address"
		})
		if th == nil {
			continue
		}
		td := findDescendant(tr, func(node *html.Node) bool {
			return node.DataAtom == atom.Td
		})
		if td == nil {
			continue
		}
		macAddress := strings.Trim(getInnerText(td), " \t\n")
		if _, ok := chartData.macAddressToDevice[macAddress]; ok {
			continue
		}
		tr = findFollowupSibling(tr)
		if tr == nil {
			continue
		}
		td = findDescendant(tr, func(node *html.Node) bool {
			return node.DataAtom == atom.Td
		})
		if td == nil {
			continue
		}
		deviceData := &DeviceData{
			DeviceName:    strings.ReplaceAll(strings.Trim(getInnerText(td), " \t\n"), "\n", ""),
			TransmitBytes: []int64{},
			ReceiveBytes:  []int64{}}
		chartData.macAddressToDevice[macAddress] = deviceData
		chartData.Devices = append(chartData.Devices, deviceData)
	}
	return nil
}

func fetchLanStatistics(chartData *ChartData) error {
	doc, err := fetchAndParse("http://192.168.1.254/cgi-bin/lanstatistics.ha")
	chartData.mu.Lock()
	defer chartData.mu.Unlock()
	chartData.FetchMilliseconds = append(chartData.FetchMilliseconds, time.Now().UnixNano()/1000000)
	if err != nil {
		return err
	}
	table := findDescendant(doc, func(node *html.Node) bool {
		return node.DataAtom == atom.Table && getAttribute(node, "summary") == "Wi-Fi Client Connection Statistics Table"
	})
	if table == nil {
		return fmt.Errorf("Table not found")
	}
	tr := findDescendant(table, func(node *html.Node) bool {
		return node.DataAtom == atom.Tr
	})
	if tr == nil {
		return fmt.Errorf("Tr not found")
	}
	for tr = findFollowupSibling(tr); tr != nil; tr = findFollowupSibling(tr) {
		columnIndex := 0
		var deviceData *DeviceData
		for td := findDescendant(tr, func(node *html.Node) bool {
			return node.DataAtom == atom.Td
		}); td != nil; td = findFollowupSibling(td) {
			if columnIndex == 0 {
				macAddress := strings.Trim(getInnerText(td), " \t\n")
				deviceData = chartData.macAddressToDevice[macAddress]
			}
			if deviceData != nil && columnIndex == 7 {
				if bytes, err := strconv.ParseInt(strings.Trim(getInnerText(td), " \t\n"), 10, 32); err == nil {
					deviceData.TransmitBytes = append(deviceData.TransmitBytes, bytes)
				}
			}
			if deviceData != nil && columnIndex == 8 {
				if bytes, err := strconv.ParseInt(strings.Trim(getInnerText(td), " \t\n"), 10, 32); err == nil {
					deviceData.ReceiveBytes = append(deviceData.TransmitBytes, bytes)
				}
			}
			columnIndex++
		}
	}
	return nil
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if data, err := ioutil.ReadFile("index.html"); err == nil {
    w.Write(data)
  } else {
		w.WriteHeader(500)
		w.Write([]byte(err.Error()))
	}
}

func dataHandler(w http.ResponseWriter, r *http.Request) {
	chartData.mu.Lock()
	data, err := json.Marshal(chartData)
	chartData.mu.Unlock()
	if err == nil {
		w.Write(data)
	} else {
		w.WriteHeader(500)
		w.Write([]byte(err.Error()))
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	chartData = &ChartData{
		macAddressToDevice: make(map[string]*DeviceData),
		Devices:            []*DeviceData{},
		FetchMilliseconds:  []int64{}}
	if err := fetchDevices(chartData); err != nil {
		log.Fatal(err)
	}
	go func() {
		for {
		  if err := fetchLanStatistics(chartData); err != nil {
				log.Print(err)
			}
		  time.Sleep(10 * time.Second)
	  }
	}()
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/data", dataHandler)
	log.Print("Serving request from http://localhost:8080")
  log.Print(http.ListenAndServe(":8080", nil))
}
