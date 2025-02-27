package main

import (
	"bufio"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh/terminal"
)

func main() {
	var year, filename, email, password string
	flag.StringVar(&year, "year", "", "Vuosiluvun kaksi viimeistä numeroa, esim. 19")
	flag.StringVar(&filename, "file", "db.csv", "Tietokannan csv-export")
	flag.Parse()

	baseURL := "https://indecs.fi/findecs" + year
	urlString := baseURL + "/users/login"

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Sähköposti: ")
	email, _ = reader.ReadString('\n')
	email = strings.TrimSpace(email)
	fmt.Print("Salasana: ")
	pwBytes, _ := terminal.ReadPassword(0)
	password = string(pwBytes)
	fmt.Println("\nTunnistaudutaan osoitteeseen", urlString)

	jar, err := cookiejar.New(nil)
	if err != nil {
		log.Fatal(err)
	}

	client := http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && via[0].URL.String() == urlString {
				return nil
			}
			return errors.New("\nTunnistautuminen epäonnistui. Tarkista käyttäjätunnus ja salasana.")
		},
		Jar: jar,
	}

	res, err := client.PostForm(urlString, url.Values{
		"data[User][email]":    []string{email},
		"data[User][password]": []string{password},
	})

	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Status)

	fmt.Printf("Reading DB file %s...\n", filename)
	fd, err := os.Open(filename)
	if err != nil {
		log.Fatal(err)
	}

	cr := csv.NewReader(fd)
	claims, err := cr.ReadAll()
	if err != nil {
		log.Fatal(err)
	}

	for _, claim := range claims {
		id := claim[0]
		receipts := strings.Split(claim[1], ";")

		fmt.Printf("Claim %s\n", id)

		f := startHTML(claim[2])

		res, err := client.Get(fmt.Sprintf(baseURL+"/CostClaims/view/%s/print", id))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(res.Status)
		bodyBytes, err := ioutil.ReadAll(res.Body)
		if err != nil {
			log.Fatal(err)
		}

		body := "<table>" + strings.Split(string(bodyBytes), "table>")[1] + "table>"
		f.WriteString(body)

		for _, receipt := range receipts {
			if receipt == "" {
				continue
			}
			fmt.Printf("> Receipt %s\n", receipt)

			// Check if the receipt is a PDF
			isPDF := strings.HasSuffix(receipt, ".pdf")
			// Check if the receipt is a PNG
			isPNG := strings.HasSuffix(receipt, ".png")
			// Check if the receipt is a JPEG (JPG)
			isJPEG := strings.HasSuffix(receipt, ".jpg") || strings.HasSuffix(receipt, ".jpeg")

			// Skip conversion for PNG and JPEG files
			if isPNG || isJPEG {
				fmt.Println("Skipping conversion for PNG and JPEG files")
				// Save the receipt file with the appropriate extension
				receiptFilePath := filepath.Join("output", receipt)
				receiptFile, err := os.Create(receiptFilePath)
				if err != nil {
					log.Fatal(err)
				}
				res, err := client.Get(baseURL + "/files/receipts/" + receipt)
				if err != nil {
					log.Fatal(err)
				}
				fmt.Printf("GET %s: %s\n", res.Request.URL, res.Status)
				io.Copy(receiptFile, res.Body)
				receiptFile.Close()
				// Add the receipt to the HTML document
				f.WriteString(fmt.Sprintf(`<img style="max-height: 1000px; max-width: 200mm; margin-bottom: 20px;" src="%s" />`, receipt))
				continue
			}

			// Download the receipt file
			res, err = client.Get(baseURL + "/files/receipts/" + receipt)
			if err != nil {
				log.Fatal(err)
			}

			fmt.Printf("GET %s: %s\n", res.Request.URL, res.Status)

			// Save the receipt file with the appropriate extension
			receiptFilePath := filepath.Join("output", receipt)
			receiptFile, err := os.Create(receiptFilePath)
			if err != nil {
				log.Fatal(err)
			}
			io.Copy(receiptFile, res.Body)
			receiptFile.Close()

			// Convert PDF receipt to images if it's a PDF
			if isPDF {
				imgFilePaths, err := convertPDFToImages(receiptFilePath)
				if err != nil {
					log.Fatal(err)
				}

				// Delete the original PDF receipt
				os.Remove(receiptFilePath)

				// Add each page of the PDF receipt to the HTML document
				for _, imgFilePath := range imgFilePaths {
					// Get the filename with the correct extension
					filename := filepath.Base(imgFilePath)
					// Add the receipt image to the HTML document
					f.WriteString(fmt.Sprintf(`<img style="max-height: 1000px; max-width: 200mm; margin-bottom: 20px;" src="%s" />`, filename))
				}
			} else {
				// Get the filename with the correct extension
				filename := filepath.Base(receiptFilePath)
				// Add the receipt image to the HTML document
				f.WriteString(fmt.Sprintf(`<img style="max-height: 1000px; max-width: 200mm; margin-bottom: 20px;" src="%s" />`, filename))
			}
		}

		f.WriteString("</body></html>")
		f.Close()
	}
}

func startHTML(filename string) *os.File {
	f, err := os.Create(filepath.Join("output", fmt.Sprintf("%s.html", filename)))
	if err != nil {
		log.Fatal(err)
	}

	f.WriteString(`
		<html>
		<head>
			<meta http-equiv='content-type' content='text/html; charset=utf-8'>
			<style>
				body {
					max-width: 200mm;
					padding: 10px;
				}
				td {
					border: 1px solid black;
					border-left: none;
					border-right: none;
					padding: 0;
				}
				tr {
					padding: 0;
				}
				table {
					padding: 0;
					border-collapse: collapse;
					width: 100%;
				}
				img {
					max-height: 1000px;
					max-width: 200mm;
					margin-bottom: 20px;
				}
			</style>
		</head>
		<body>`)

	return f
}

func convertPDFToImages(pdfFilePath string) ([]string, error) {
	outputDir := "output"

	// Create the output directory if it doesn't exist
	err := os.MkdirAll(outputDir, os.ModePerm)
	if err != nil {
		return nil, err
	}

	// Convert the PDF to images using ImageMagick's `convert` command
	outputPrefix := strings.TrimSuffix(filepath.Base(pdfFilePath), filepath.Ext(pdfFilePath))
	outputFilePath := filepath.Join(outputDir, outputPrefix+"-%d.png")
	cmd := exec.Command("convert", "-density", "200", "-quality", "200", pdfFilePath, outputFilePath)
	err = cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			log.Println("convert command failed with exit status:", exitErr.ExitCode())
			log.Println("Error message:", string(exitErr.Stderr))
		}
		return nil, err
	}

	// Get the list of image files in the output directory
	files, err := ioutil.ReadDir(outputDir)
	if err != nil {
		return nil, err
	}

	var imagePaths []string
	for _, file := range files {
		if !file.IsDir() && strings.HasPrefix(file.Name(), outputPrefix) {
			imagePaths = append(imagePaths, filepath.Join(outputDir, file.Name()))
		}
	}

	return imagePaths, nil
}
