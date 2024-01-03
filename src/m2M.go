package main

import (
	"bufio"
	"encoding/gob"
	"flag"
	"fmt"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"net"
	"os"
	"strings"
	"time"
    "errors"
    "github.com/int128/slack"
)

type Receiver struct {
	Mail    string `yaml:"mail"`
	UrlHook string `yaml:"url_hook"`
}

/*
- mail: test1@mail.com
  url_hook: http://test.com/hook1
- mail: test2@mail.com
  url_hook: http://test.com/hook2
- mail: test3@mail.com
  url_hook: http://test.com/hook3
*/

type Mail struct {
	From string
	To   string
	Subject string
	Data string
}

type NotificationPayload struct {
	Text string `json:"text"`
}


var debug bool
var notify bool
const MAILDIR string = "./mails"
const LOGSDIR string = "./logs"
const CONFIGDIR string = "./config"



func main() {
	flag.BoolVar(&debug, "d", false, "enable debug output")
	flag.BoolVar(&notify, "n", false, "enable notify output")
	flag.Parse()

	// creating LOGSDIR folder 
	err := createDirectory(LOGSDIR)
	if err != nil {
		fmt.Println(err)
		return
	}
	// creating logfile 
	if _, err := os.Stat(LOGSDIR + "/" + os.Args[0]+".log"); os.IsNotExist(err) {
		file, err := os.Create(LOGSDIR + "/" + os.Args[0]+".log")
		if err != nil {
			fmt.Println(err)
			return
		}
		defer file.Close()

		fmt.Println("File created successfully")
	} else {
		fmt.Println("File ", LOGSDIR + "/" + os.Args[0]+".log", "already exists")
	}
	logFile, err := os.OpenFile(LOGSDIR + "/" + os.Args[0]+".log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0700)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer logFile.Close()

	// creating MAILDIR folder 
	err = createDirectory(MAILDIR)
	if err != nil {
		handleError(err)
		return
	}

	log.SetOutput(logFile)

	receivers := []Receiver{}
	data, err := ioutil.ReadFile(CONFIGDIR + "/" + "config.yml")
	if err != nil {
		handleError(err)
		return
	}
	err = yaml.Unmarshal(data, &receivers)
	if err != nil {
		handleError(err)
		return
	}


	fmt.Println("Configuration:")
	for _,receiver :=  range receivers {
		fmt.Println(receiver.Mail,receiver.UrlHook)
	}

	listener, err := net.Listen("tcp", "localhost:2525")
	if err != nil {
		handleError(err)
		return
	}

	if notify {
		go func() {
			for {
				time.Sleep(5 * time.Second)
				files, err := ioutil.ReadDir(MAILDIR+"/")
				if err != nil {
					handleError(err)
					continue
				}

				for _, file := range files {
					if strings.HasSuffix(file.Name(), ".mail.serialized") {
						mailFile, err := os.Open(MAILDIR+"/"+file.Name())
						if err != nil {
							handleError(err)
							continue
						}

						mail := &Mail{}
						dec := gob.NewDecoder(mailFile)
						err = dec.Decode(mail)
						if err != nil {
							handleError(err)
							mailFile.Close()
							continue
						}

						urlHook := getUrlHook(receivers, mail.To)

						fmt.Println("Mail:", mail.To, "Subject:", mail.Subject, "URL Hook:", urlHook)

						mailFile.Close()
						if len(urlHook) > 3 {
							if len(mail.Subject) > 3{
								err := notifyMattermost(mail.Subject , urlHook)
								if err != nil {
									handleError(err)
								}else{
									// delete file.Name
									status := safeDeleteFile(MAILDIR+"/"+file.Name())
									if status != nil {
									handleError(status)
									}
								}
							}

						}
						time.Sleep(2 * time.Second)
					}
				}
			}
		}()
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			handleError(err)
			return
		}

		log.Println("New connection:", conn.RemoteAddr())

		go handleConnection(conn, receivers)
	}
}
func handleConnection(conn net.Conn, receivers []Receiver) {
	defer conn.Close()

	mail := &Mail{}
	reader := bufio.NewReader(conn)
	conn.Write([]byte("220 localhost Service ready\r\n"))

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			handleError(err)
			return
		}

		log.Println("Received:", line)

		switch {
		case strings.HasPrefix(line, "HELO"), strings.HasPrefix(line, "EHLO"):
			conn.Write([]byte("250 localhost\r\n"))
		case strings.HasPrefix(line, "MAIL FROM"):
			mail.From = line[10 : len(line)-2]
			conn.Write([]byte("250 OK\r\n"))
		case strings.HasPrefix(line, "RCPT TO"):
			mail.To = line[8 : len(line)-2]
			conn.Write([]byte("250 OK\r\n"))
		case strings.HasPrefix(line, "DATA"):
			conn.Write([]byte("354 Start mail input; end with <CRLF>.<CRLF>\r\n"))
			mail.Data = handleDataCommand(reader, conn, mail, receivers)
			fmt.Println("[+] Mail:", mail.To, "Subject:",mail.Subject)
		case strings.HasPrefix(line, "RSET"):
			mail = &Mail{}
			conn.Write([]byte("250 OK\r\n"))
		case strings.HasPrefix(line, "NOOP"):
			conn.Write([]byte("250 OK\r\n"))
		case strings.HasPrefix(line, "QUIT"):
			conn.Write([]byte("221 localhost Service closing transmission channel\r\n"))
			return
		default:
			conn.Write([]byte("502 Command not implemented\r\n"))
		}
	}
}

func handleDataCommand(reader *bufio.Reader, conn net.Conn, mail *Mail, receivers []Receiver) string {
	data := ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			handleError(err)
			return ""
		}

		if line == ".\r\n" {
			timestamp := time.Now().Format("20060102150405")
			file, err := os.Create(MAILDIR + "/" + timestamp + ".txt")
			if err != nil {
				handleError(err)
				return ""
			}
			defer file.Close()

			file.WriteString(data)
			conn.Write([]byte("250 OK\r\n"))

			mailFile, err := os.Create(MAILDIR + "/" + timestamp + ".mail.serialized")
			if err != nil {
				handleError(err)
				return ""
			}
			defer mailFile.Close()

			enc := gob.NewEncoder(mailFile)
			mail.Data = data
			mail.Subject = extractSubject(string(mail.Data))
			err = enc.Encode(mail)
			if err != nil {
				handleError(err)
				return ""
			}

			/* DO IT NOW */

			break
		}

		data += line
	}

	return data
}

func getUrlHook(receivers []Receiver, to string) string {
	// Define a regex to match an email address
	re := regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)

	// Extract the email address
	matches := re.FindStringSubmatch(to)
	if len(matches) > 0 {
		to = matches[0]
	}

	for _, receiver := range receivers {
		if receiver.Mail == to {
			return receiver.UrlHook
		}
	}
	return ""
}

func handleError(errorString error){
	if (debug == true){
		log.Println(errorString)
	}
	fmt.Println(errorString)
}
func extractSubject(mailData string) string {
	scanner := bufio.NewScanner(strings.NewReader(mailData))
	if err := scanner.Err(); err != nil {
		fmt.Println("Error while reading:", err)
		return ""
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Subject:") {
			return line[8 : len(line)]	
		}
	}
	return ""
}
func notifyMattermost(message string, receiver string) error {

	if err := slack.Send(receiver, &slack.Message{
		Username:	"m2M",
		IconEmoji:	":email:",
		Text:	message,
	}); err != nil {
		log.Fatalf("Could not send the message: %s", err)
	}
	return nil
}
func createDirectory(path string) error{
	if _, err := os.Stat(path); os.IsNotExist(err) {
		err = os.Mkdir(path, 0700)
		if err != nil {
			handleError(err)
			return err
		}
	}
	return nil
}
func safeDeleteFile(filePath string) error{
    if _, err := os.Stat(filePath); errors.Is(err, os.ErrNotExist) {
    	fmt.Printf("File %s does not exist, no need to delete.\n", filePath)
    	return err
    } else {
        err := os.Remove(filePath)
        if err != nil {
            log.Fatal(err)
            return err
        } else {
            fmt.Printf("File %s deleted successfully.\n", filePath)
        }
    }
    return nil
}

/* made with bing */
