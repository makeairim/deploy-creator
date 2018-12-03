package sshConnection
/*
Author Bartosz Wołcerz
 */
import (
	"os/user"
	"io/ioutil"
	"fmt"
	"encoding/pem"
	"crypto/x509"
	"io"
	"sync"
	"golang.org/x/crypto/ssh"
	"log"
	"strconv"
)

type ConnectionInt interface {
	Execute(cmd Command) string
	IsSuccess() bool
	Valid()
}
type Command struct {
	Cmd string
}
type Client struct {
	Host         string
	ClientConfig *ssh.ClientConfig
	Session      *ssh.Session
	Conn         ssh.Conn
}

type ConnectionConfiguration struct {
	User            string
	Password        string
	Remote          bool
	AddressWithPort string
}
type connectionChan struct {
	in  chan<- string
	out <-chan string
}

func (conn *connectionChan) Execute(cmd Command) string {
	conn.in <- cmd.Cmd
	fmt.Println(cmd.Cmd + " executed")
	return <-conn.out
}
func (conn *connectionChan) IsSuccess() bool {
	out := conn.Execute(Command{"echo $?"})
	code, err := strconv.Atoi(out[:1])
	return err == nil && code == 0
}

func (conn *connectionChan) Valid() {
	if conn.IsSuccess() != true {
		panic("Something went wrong")
	}
}
func ReadPrivateKey(password string) ([]byte, error) {
	usr, _ := user.Current()
	path := usr.HomeDir + "/.ssh/id_rsa"
	privateKey, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load identity: %v", err)
	}

	block, rest := pem.Decode(privateKey)
	if len(rest) > 0 {
		return nil, fmt.Errorf("extra data when decoding private key")
	}
	if !x509.IsEncryptedPEMBlock(block) {
		return privateKey, nil
	}

	passphrase := []byte (password)
	der, err := x509.DecryptPEMBlock(block, passphrase)
	if err != nil {
		return nil, fmt.Errorf("decrypt failed: %v", err)
	}

	privateKey = pem.EncodeToMemory(&pem.Block{
		Type:  block.Type,
		Bytes: der,
	})

	return privateKey, nil
}
func muxShell(w io.Writer, r io.Reader) (chan<- string, <-chan string) {
	in := make(chan string, 1)
	out := make(chan string, 1)

	var wg sync.WaitGroup
	wg.Add(1) //for the shell itself

	go func() {
		for cmd := range in {

			wg.Add(1)
			w.Write([]byte(cmd + "\n"))
			wg.Wait()
		}
	}()
	go func() {
		const bufSize = 65 * 1024
		var (
			buf [bufSize]byte
			t   int
		)
		for {
			if t == bufSize {
				t = 0
			}
			n, err := r.Read(buf[t:])

			if err != nil {
				close(in)
				close(out)
				return
			}
			t += n
			if t >= bufSize {
				t = bufSize
			}
			if buf[t-2] == '$' || buf[t-2] == ':' { //assuming the $PS1 == '$ ' or passwords == ': '
				out <- string(buf[:t])
				t = 0
				wg.Done()
			}
		}
	}()
	return in, out
}

// Connects to the remote SSH server, returns error if it couldn't establish a session to the SSH server
func (a *Client) Connect() error {
	client, err := ssh.Dial("tcp", a.Host, a.ClientConfig)
	if err != nil {
		return err
	}

	a.Conn = client.Conn
	a.Session, err = client.NewSession()
	if err != nil {
		return err
	}
	return nil
}

func GetSSHConnectionConfig(userConfig *ConnectionConfiguration) (Client) {
	method := make([]ssh.AuthMethod, 1)
	if userConfig.Remote {
		per, err := ReadPrivateKey(userConfig.Password)
		if err != nil {
			panic("Cannot read ssh key" + err.Error())
		}
		key, err := ssh.ParsePrivateKey(per)
		if err != nil {
			panic("Cannot read ssh key" + err.Error())
		}
		method[0] = ssh.PublicKeys(key)
	} else {
		method[0] = ssh.Password(userConfig.Password)
	}
	config := ssh.ClientConfig{
		User:            userConfig.User,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Auth:            method,
	}
	return Client{ClientConfig: &config, Host: userConfig.AddressWithPort}
}
func (client *Client) RunCommands(getCmds func(con ConnectionInt, params map[string]string) []func(), parameters map[string]string) {
	session := client.Session
	//defer session.Close()
	//var b bytes.Buffer
	//session.Stdout = &b
	//
	//if err := session.Run("/usr/bin/ls"); err != nil {
	//	panic("Failed to run: " + err.Error())
	//}
	//fmt.Println(b.String())
	modes := ssh.TerminalModes{
		ssh.ECHO:          0,     // disable echoing
		ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
		ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
	}

	if err := session.RequestPty("xterm", 80, 40, modes); err != nil {
		log.Fatal(err)
	}

	w, err := session.StdinPipe()
	if err != nil {
		panic(err)
	}
	r, err := session.StdoutPipe()
	if err != nil {
		panic(err)
	}
	in, out := muxShell(w, r)
	if err := session.Start("/bin/bash"); err != nil {
		log.Fatal(err)
	}
	<-out //ignore the shell output

	conn := connectionChan{in, out}
	commands := getCmds(&conn, parameters)
	run(commands)
	session.Wait()
}

func run(commands []func()) {
	for _, i := range commands {
		i()
	}
}
func GetClient(parameters map[string]string) Client {

	conf := ConnectionConfiguration{
		Password:        parameters["remote-host-user-password"],
		Remote:          parameters["use-key"] == "true" || parameters["useKey"] == "TRUE",
		User:            parameters["remote-host-user"],
		AddressWithPort: parameters["remote-addr"] + ":" + parameters["remote-port"]}
	client := GetSSHConnectionConfig(&conf)
	return client
}

func (a *Client) Close() {
	a.Session.Close()
	a.Conn.Close()
}
func getKeyFile() (key ssh.Signer, err error) {
	usr, _ := user.Current()
	file := usr.HomeDir + "/.ssh/id_rsa"
	buf, err := ioutil.ReadFile(file)
	if err != nil {
		return
	}
	key, err = ssh.ParsePrivateKey(buf)
	if err != nil {
		return
	}
	return
}
