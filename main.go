package main

// Author: Jim Conning, Jan 2017

import (
	"fmt"
	"net/http"
	"io/ioutil"
	"encoding/json"
	"sync"
	"errors"
	"sort"
	"flag"
	"log"
	"strconv"
)

var maxPrime int
var numExecutions int
var numLoops int
var maxConcurrency int
var lambdaFunctions = map[int]string{}
func init() {
	flag.IntVar(&maxPrime, "max", 1000000, "maximum number to search for primes (<=2M to not cause out of memory in the lowest Lambda memory setting)")
	flag.IntVar(&numExecutions, "execs", 20, "number of times to execute the Lambda function")
	flag.IntVar(&numLoops, "loops", 1, "number of times to repeat the search for primes (without consuming additional memory)")
	flag.IntVar(&maxConcurrency, "conc", 100, "limit of concurrently running Lambda functions")
	flag.Parse()

	parseConfig()
}

type execution struct {
	DurationSeconds float64
	memory int
}

// AWS Lambda pricing in USD as of Jan 2017
var costPerRequest float64 = 0.0000002
var costPerGbSeconds float64 = 0.00001667 

func parseConfig() {
	contents, err := ioutil.ReadFile("config.json")
	if err != nil {
		log.Fatal(err)
	}

	var config interface{}
	err = json.Unmarshal(contents, &config);
	if err != nil {
		log.Fatal(err)
	}

	// Parse the json config using the standard library
	funcConfig := config.(map[string]interface{})
	functions := funcConfig["functions"].(map[string]interface{})
	for key, value := range functions {
		mem, err := strconv.Atoi(key)
		if err != nil {
			log.Fatal(err)
		}
		url := value.(string)
		lambdaFunctions[mem] = url
	}
}

func triggerLambda(url string, mem int, max int, loops int) (execution, error) {
	var e execution
	e.memory = mem

	resp, err := http.Get(fmt.Sprintf("%s?max=%d&loops=%d", url, max, loops));
	if err != nil {
		return e, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return e, errors.New(fmt.Sprintf("status code: %d", resp.StatusCode))
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return e, err
	}
	err = json.Unmarshal(body, &e)

	return e, nil
}

func invokeLambda(executions chan execution) int {
	var wg sync.WaitGroup
	var tokens = make(chan struct{}, maxConcurrency) // counting semaphore used to enforce a concurrency limit on calls to Lambda
	var lambdaErrors int

	fmt.Printf("Triggering %d Lambda functions %d times each, all in parallel\n", len(lambdaFunctions), numExecutions)
	fmt.Printf("Each function will loop %d time(s) and in each loop calculate all primes <=%d\n", numLoops, maxPrime)
	fmt.Println("Working...")
	for mem, url := range lambdaFunctions {
		for c := 0; c < numExecutions; c++ {
			wg.Add(1)
			go func(u string, m int) {
				defer wg.Done()
				tokens <- struct{}{} // acquire a token
				e, err := triggerLambda(u, m, maxPrime, numLoops)
				<-tokens // release the token
				if err != nil {
					fmt.Println(err)
					lambdaErrors++
				}
				executions <- e
			}(url, mem)
		}
	}

	// Wait for all goroutines to finish their work
	go func() {
		wg.Wait()
		close(executions)
	}()

	return lambdaErrors
}

func displayResults(executions chan execution, lambdaErrors int) {
	var totalDurations map[int]float64 = make(map[int]float64)
	var executionCounts map[int]int = make(map[int]int)

	// Pull all execution results from the channel
	for e := range executions {
		if e.DurationSeconds > 0 { // only count executions that didn't error
			totalDurations[e.memory] += e.DurationSeconds
			executionCounts[e.memory]++
		}
	}

	// Sort the various lambda function memory sizes for pretty printing
	var memories []int
	for mem, _ := range lambdaFunctions {
		memories = append(memories, mem)
	}
	sort.Ints(memories)

	// Display results
	fmt.Printf("Number of lambda executions returning errors: %d\n", lambdaErrors)
	fmt.Println("Stats for each Lambda function by Lambda memory allocation:")
	var totalCost float64
	for _, mem := range memories {
		cost := float64(executionCounts[mem]) * costPerRequest + 
			(float64(mem)/float64(1024)) * float64(totalDurations[mem]) * costPerGbSeconds // convert duration to GB-seconds
		totalCost += cost
		fmt.Printf("  %dmb %fsec(avg) $%f(total) to calculate %d times all prime numbers <=%d\n", 
			mem, totalDurations[mem]/float64(executionCounts[mem]), cost, executionCounts[mem], maxPrime)
	}
	fmt.Printf("Total cost of this test run: $%f\n", totalCost);
}

func main() {
	executions := make(chan execution)

	lambdaErrors := invokeLambda(executions)

	displayResults(executions, lambdaErrors)
}

