package main

import (
	"./common"
	"./nat"
	"bufio"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var accessKey = flag.String("key", "", "please login into dog-tunnel.tk to get accesskey")
var clientKey = flag.String("clientkey", "", "when other client linkt to the reg client, need clientkey, or empty")

var serverAddr = flag.String("remote", "dog-tunnel.tk:8008", "connect remote server")
var addInitAddr = flag.String("addip", "127.0.0.1", "addip for bust,xx.xx.xx.xx;xx.xx.xx.xx;")
var pipeNum = flag.Int("pipen", 1, "pipe num for transmission")

var serveName = flag.String("reg", "", "reg the name for client link, must assign reg or link")

var linkName = flag.String("link", "", "name for link, must assign reg or link")
var localAddr = flag.String("local", "", "addr for listen or connect(value \"socks5\" means tcp socks5 proxy for reg),depends on link or reg")
var bVerbose = flag.Bool("v", false, "verbose mode")
var delayTime = flag.Int("delay", 2, "if bust fail, try to make some delay seconds")
var clientMode = flag.Int("mode", 0, "connect mode:0 if p2p fail, use c/s mode;1 just p2p mode;2 just c/s mode")
var bUseSSL = flag.Bool("ssl", true, "use ssl")
var bShowVersion = flag.Bool("version", false, "show version")
var bLoadSettingFromFile = flag.Bool("f", false, "load setting from file(~/.dtunnel)")

var remoteConn net.Conn
var clientType = -1

var g_ClientMap map[string]*Client
var g_Id2UDPSession map[string]*UDPMakeSession
var markName = ""
var bForceQuit = false

func isCommonSessionId(id string) bool {
	return id == "common"
}

func handleResponse(conn net.Conn, clientId string, action string, content string) {
	//log.Println("got", clientId, action)
	switch action {
	case "show":
		fmt.Println(time.Now().Format("2006-01-02 15:04:05"), content)
	case "showandretry":
		fmt.Println(time.Now().Format("2006-01-02 15:04:05"), content)
		remoteConn.Close()
	case "showandquit":
		fmt.Println(time.Now().Format("2006-01-02 15:04:05"), content)
		remoteConn.Close()
		bForceQuit = true
	case "clientquit":
		client := g_ClientMap[clientId]
		log.Println("clientquit!!!", clientId, client)
		if client != nil {
			client.Quit()
		}
	case "remove_udpsession":
		log.Println("server force remove udpsession", clientId)
		delete(g_Id2UDPSession, clientId)
	case "query_addrlist_a":
		outip := content
		arr := strings.Split(clientId, "-")
		id := arr[0]
		sessionId := arr[1]
		pipeType := arr[2]
		g_Id2UDPSession[id] = &UDPMakeSession{id: id, sessionId: sessionId, pipeType: pipeType}
		go g_Id2UDPSession[id].reportAddrList(true, outip)
	case "query_addrlist_b":
		arr := strings.Split(clientId, "-")
		id := arr[0]
		sessionId := arr[1]
		pipeType := arr[2]
		g_Id2UDPSession[id] = &UDPMakeSession{id: id, sessionId: sessionId, pipeType: pipeType}
		go g_Id2UDPSession[id].reportAddrList(false, content)
	case "tell_bust_a":
		session, bHave := g_Id2UDPSession[clientId]
		if bHave {
			go session.beginMakeHole(content)
		}
	case "tell_bust_b":
		session, bHave := g_Id2UDPSession[clientId]
		if bHave {
			go session.beginMakeHole("")
		}
	case "csmode_c_tunnel_close":
		log.Println("receive close msg from server")
		arr := strings.Split(clientId, "-")
		clientId = arr[0]
		sessionId := arr[1]
		client, bHave := g_ClientMap[clientId]
		if bHave {
			client.removeSession(sessionId)
		}
	case "csmode_s_tunnel_close":
		arr := strings.Split(clientId, "-")
		clientId = arr[0]
		sessionId := arr[1]
		client, bHave := g_ClientMap[clientId]
		if bHave {
			client.removeSession(sessionId)
		}
	case "csmode_s_tunnel_open":
		oriId := clientId
		arr := strings.Split(oriId, "-")
		clientId = arr[0]
		sessionId := arr[1]
		client, bHave := g_ClientMap[clientId]
		if !bHave {
			client = &Client{id: clientId, pipes: make(map[int]net.Conn), engine: nil, buster: true, sessions: make(map[string]*clientSession), ready: true, bUdp: false}
			client.pipes[0] = remoteConn
			g_ClientMap[clientId] = client
		} else {
			client.pipes[0] = remoteConn
			client.ready = true
			client.bUdp = false
		}
		//log.Println("client init csmode", clientId, sessionId)
		if *localAddr != "socks5" {
			s_conn, err := net.DialTimeout("tcp", *localAddr, 10*time.Second)
			if err != nil {
				log.Println("connect to local server fail:", err.Error())
				msg := "cannot connect to bind addr" + *localAddr
				common.Write(remoteConn, clientId, "tunnel_error", msg)
				//remoteConn.Close()
				return
			} else {
				client.sessions[sessionId] = &clientSession{pipe: remoteConn, localConn: s_conn}
				go handleLocalPortResponse(client, oriId)
			}
		} else {
			client.sessions[sessionId] = &clientSession{pipe: remoteConn, localConn: nil, status: "init", recvMsg: ""}
		}
	case "csmode_c_begin":
		client, bHave := g_ClientMap[clientId]
		if !bHave {
			client = &Client{id: clientId, pipes: make(map[int]net.Conn), engine: nil, buster: false, sessions: make(map[string]*clientSession), ready: true, bUdp: false}
			client.pipes[0] = remoteConn
			g_ClientMap[clientId] = client
		} else {
			client.pipes[0] = remoteConn
			client.ready = true
			client.bUdp = false
		}
		if client.MultiListen() {
			common.Write(remoteConn, clientId, "makeholeok", "csmode")
		}
	case "csmode_msg_c":
		oriId := clientId
		arr := strings.Split(clientId, "-")
		clientId = arr[0]
		sessionId := arr[1]
		client, bHave := g_ClientMap[clientId]
		if bHave {
			session := client.getSession(sessionId)
			if session != nil && session.localConn != nil {
				session.localConn.Write([]byte(content))
			} else if session != nil && *localAddr == "socks5" {
				session.processSockProxy(client, oriId, content, func() {
					if len(session.recvMsg) > 0 && session.localConn != nil {
						session.localConn.Write([]byte(session.recvMsg))
					}
				})
			}
		}
	case "csmode_msg_s":
		arr := strings.Split(clientId, "-")
		clientId = arr[0]
		sessionId := arr[1]
		client, bHave := g_ClientMap[clientId]
		if bHave {
			session := client.getSession(sessionId)
			if session != nil && session.localConn != nil {
				session.localConn.Write([]byte(content))
			} else {
				log.Println("cs:cannot tunnel msg", sessionId)
			}
		}
	}
}

type UDPMakeSession struct {
	id        string
	sessionId string
	buster    bool
	engine    *nat.AttemptEngine
	delay     int
	pipeType  string
}

func (session *UDPMakeSession) beginMakeHole(content string) {
	engine := session.engine
	if engine == nil {
		return
	}
	addrList := content
	if session.buster {
		engine.SetOtherAddrList(addrList)
	}
	log.Println("begin bust", session.id, session.sessionId, session.buster)
	if clientType == 1 && !session.buster {
		log.Println("retry bust!")
	}
	report := func() {
		if session.buster {
			if session.delay > 0 {
				log.Println("try to delay", session.delay, "seconds")
				time.Sleep(time.Duration(session.delay) * time.Second)
			}
			go common.Write(remoteConn, session.id, "success_bust_a", "")
		}
	}
	oldSession := session
	conn, err := engine.GetConn(report)
	session, _bHave := g_Id2UDPSession[session.id]
	if session != oldSession {
		return
	}
	if !_bHave {
		return
	}
	delete(g_Id2UDPSession, session.id)
	if err == nil {
		if !session.buster {
			common.Write(remoteConn, session.id, "makeholeok", "")
		}
		client, bHave := g_ClientMap[session.sessionId]
		if !bHave {
			client = &Client{id: session.sessionId, engine: session.engine, buster: session.buster, ready: true, bUdp: true, sessions: make(map[string]*clientSession), specPipes: make(map[string]net.Conn), pipes: make(map[int]net.Conn)}
			g_ClientMap[session.sessionId] = client
		}
		if isCommonSessionId(session.pipeType) {
			size := len(client.pipes)
			client.pipes[size] = conn
			go client.Run(size, "")
			log.Println("add common session", session.buster, session.sessionId, session.id)
			if clientType == 1 {
				if len(client.pipes) == *pipeNum {
					client.MultiListen()
				}
			}
		} else {
			client.specPipes[session.pipeType] = conn
			go client.Run(-1, session.pipeType)
			log.Println("add session for", session.pipeType)
		}
	} else {
		delete(g_ClientMap, session.sessionId)
		log.Println("cannot connect", err.Error())
		if !session.buster && err.Error() != "quit" {
			common.Write(remoteConn, session.id, "makeholefail", "")
		}
	}
}

func (session *UDPMakeSession) reportAddrList(buster bool, outip string) {
	id := session.id
	var otherAddrList string
	if !buster {
		arr := strings.SplitN(outip, ":", 2)
		outip, otherAddrList = arr[0], arr[1]
	} else {
		arr := strings.SplitN(outip, ":", 2)
		var delayTime string
		outip, delayTime = arr[0], arr[1]
		session.delay, _ = strconv.Atoi(delayTime)
		if session.delay < 0 {
			session.delay = 0
		}
	}
	outip += ";" + *addInitAddr
        _id, _ := strconv.Atoi(id)
	engine, err := nat.Init(outip, buster, _id)
	if err != nil {
		println("init error", err.Error())
		disconnect()
		return
	}
	session.engine = engine
	session.buster = buster
	if !buster {
		engine.SetOtherAddrList(otherAddrList)
	}
	addrList := engine.GetAddrList()
	common.Write(remoteConn, id, "report_addrlist", addrList)
}

type fileSetting struct {
	Key string
}

func saveSettings(info fileSetting) error {
	clientInfoStr, err := json.Marshal(info)
	if err != nil {
		return err
	}
	user, err := user.Current()
	if err != nil {
		return err
	}
	filePath := path.Join(user.HomeDir, ".dtunnel")

	return ioutil.WriteFile(filePath, clientInfoStr, 0700)
}

func loadSettings(info *fileSetting) error {
	user, err := user.Current()
	if err != nil {
		return err
	}
	filePath := path.Join(user.HomeDir, ".dtunnel")
	content, err := ioutil.ReadFile(filePath)
	if err != nil {
		return err
	}
	err = json.Unmarshal([]byte(content), info)
	if err != nil {
		return err
	}
	return nil
}

func main() {
	flag.Parse()
	if *bShowVersion {
		fmt.Printf("%.2f\n", common.Version)
		return
	}
	if !*bVerbose {
		log.SetOutput(ioutil.Discard)
	}
	if *serveName == "" && *linkName == "" {
		println("you must assign reg or link")
		return
	}
	if *serveName != "" && *linkName != "" {
		println("you must assign reg or link, not both of them")
		return
	}
	if *localAddr == "" {
		println("you must assign the local addr")
		return
	}
	if *serveName != "" {
		clientType = 0
	} else {
		clientType = 1
	}
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		n := 0
		for {
			<-c
			log.Println("received signal,shutdown")
			bForceQuit = true
			if remoteConn != nil {
				remoteConn.Close()
			}
			n++
			if n > 5 {
				log.Println("force shutdown")
				os.Exit(-1)
			}
		}
	}()

	loop := func() bool {
		if bForceQuit {
			return true
		}
		g_ClientMap = make(map[string]*Client)
		g_Id2UDPSession = make(map[string]*UDPMakeSession)
		//var err error
		if *bUseSSL {
			_remoteConn, err := tls.Dial("tcp", *serverAddr, &tls.Config{InsecureSkipVerify: true})
			if err != nil {
				println("connect remote err:" + err.Error())
				return false
			}
			remoteConn = net.Conn(_remoteConn)
		} else {
			_remoteConn, err := net.DialTimeout("tcp", *serverAddr, 10*time.Second)
			if err != nil {
				println("connect remote err:" + err.Error())
				return false
			}
			remoteConn = _remoteConn
		}
		println("connect to server succeed")
		go connect()
		q := make(chan bool)
		go func() {
			c := time.Tick(time.Second * 10)
		out:
			for {
				select {
				case <-c:
					if remoteConn != nil {
						common.Write(remoteConn, "-1", "ping", "")
					}
				case <-q:
					break out
				}
			}
		}()

		common.Read(remoteConn, handleResponse)
		q <- true
		for clientId, client := range g_ClientMap {
			log.Println("client shutdown", clientId)
			client.Quit()
		}

		if remoteConn != nil {
			remoteConn.Close()
		}
		if bForceQuit {
			return true
		}
		return false
	}
	if clientType == 0 {
		for {
			if loop() {
				break
			}
			time.Sleep(10 * time.Second)
		}
	} else {
		loop()
	}
	log.Println("service shutdown")
}

func connect() {
	if *pipeNum <= 0 {
		*pipeNum = 1
	}
	clientInfo := common.ClientSetting{Version: common.Version, Delay: *delayTime, Mode: *clientMode, PipeNum: *pipeNum, AccessKey: *accessKey, ClientKey: *clientKey}
	if *bLoadSettingFromFile {
		var setting fileSetting
		err := loadSettings(&setting)
		if err == nil {
			clientInfo.AccessKey = setting.Key
		} else {
			log.Println("load setting fail", err.Error())
		}
	} else {
		if clientInfo.AccessKey != "" {
			var setting = fileSetting{Key: clientInfo.AccessKey}
			err := saveSettings(setting)
			if err != nil {
				log.Println("save setting error", err.Error())
			} else {
				println("save setting ok, nexttime please use -f to replace -key")
			}
		}
	}
	if clientType == 0 {
		markName = *serveName
		clientInfo.ClientType = "reg"
	} else if clientType == 1 {
		markName = *linkName
		clientInfo.ClientType = "link"
	} else {
		println("no clienttype!")
	}
	clientInfo.Name = markName
	clientInfoStr, err := json.Marshal(clientInfo)
	if err != nil {
		println("encode args error")
	}
	log.Println("init client", string(clientInfoStr))
	common.Write(remoteConn, "0", "init", string(clientInfoStr))
}

func disconnect() {
	if remoteConn != nil {
		remoteConn.Close()
		remoteConn = nil
	}
}

type clientSession struct {
	pipe      net.Conn
	localConn net.Conn
	status    string
	recvMsg   string
	extra     uint8
}

func (session *clientSession) processSockProxy(sc *Client, sessionId, content string, callback func()) {
	pipe := session.pipe
	session.recvMsg += content
	bytes := []byte(session.recvMsg)
	size := len(bytes)
	//log.Println("recv msg-====", len(session.recvMsg),  session.recvMsg, session.status, sessionId)
	switch session.status {
	case "init":
		if session.localConn != nil {
			session.localConn.Close()
			session.localConn = nil
		}
		if size < 2 {
			//println("wait init")
			return
		}
		var _, nmethod uint8 = bytes[0], bytes[1]
		//println("version", version, nmethod)
		session.status = "version"
		session.recvMsg = string(bytes[2:])
		session.extra = nmethod
	case "version":
		if uint8(size) < session.extra {
			//println("wait version")
			return
		}
		var send = []uint8{5, 0}
		go common.Write(pipe, sessionId, "tunnel_msg_s", string(send))
		session.status = "hello"
		session.recvMsg = string(bytes[session.extra:])
		session.extra = 0
		//log.Println("now", len(session.recvMsg))
	case "hello":
		var hello reqMsg
		bOk, tail := hello.read(bytes)
		if bOk {
			go func() {
				var ansmsg ansMsg
				s_conn, err := net.DialTimeout(hello.reqtype, hello.url, 10*time.Second)
				if err != nil {
					log.Println("connect to local server fail:", err.Error())
					//msg := "cannot connect to bind addr" + *localAddr
					ansmsg.gen(&hello, 4)
					go common.Write(pipe, sessionId, "tunnel_msg_s", string(ansmsg.buf[:ansmsg.mlen]))
					//common.Write(pipe, sessionId, "tunnel_error", msg)
					return
				} else {
					session.localConn = s_conn
					go handleLocalPortResponse(sc, sessionId)
					ansmsg.gen(&hello, 0)
					go common.Write(pipe, sessionId, "tunnel_msg_s", string(ansmsg.buf[:ansmsg.mlen]))
					session.status = "ok"
					session.recvMsg = string(tail)
					callback()
				}
			}()
		} else {
			//log.Println("wait hello")
		}
		return
	case "ok":
		return
	}
	session.processSockProxy(sc, sessionId, "", callback)
}

type ansMsg struct {
	ver  uint8
	rep  uint8
	rsv  uint8
	atyp uint8
	buf  [300]uint8
	mlen uint16
}

func (msg *ansMsg) gen(req *reqMsg, rep uint8) {
	msg.ver = 5
	msg.rep = rep //rfc1928
	msg.rsv = 0
	msg.atyp = 1 //req.atyp

	msg.buf[0], msg.buf[1], msg.buf[2], msg.buf[3] = msg.ver, msg.rep, msg.rsv, msg.atyp
	for i := 5; i < 11; i++ {
		msg.buf[i] = 0
	}
	msg.mlen = 10
}

type reqMsg struct {
	ver       uint8     // socks v5: 0x05
	cmd       uint8     // CONNECT: 0x01, BIND:0x02, UDP ASSOCIATE: 0x03
	rsv       uint8     //RESERVED
	atyp      uint8     //IP V4 addr: 0x01, DOMANNAME: 0x03, IP V6 addr: 0x04
	dst_addr  [255]byte //
	dst_port  [2]uint8  //
	dst_port2 uint16    //

	reqtype string
	url     string
}

func (msg *reqMsg) read(bytes []byte) (bool, []byte) {
	size := len(bytes)
	if size < 4 {
		return false, bytes
	}
	buf := bytes[0:4]

	msg.ver, msg.cmd, msg.rsv, msg.atyp = buf[0], buf[1], buf[2], buf[3]
	//println("test", msg.ver, msg.cmd, msg.rsv, msg.atyp)

	if 5 != msg.ver || 0 != msg.rsv {
		log.Println("Request Message VER or RSV error!")
		return false, bytes[4:]
	}
	buf = bytes[4:]
	size = len(buf)
	switch msg.atyp {
	case 1: //ip v4
		if size < 4 {
			return false, buf
		}
		copy(msg.dst_addr[:4], buf[:4])
		buf = buf[4:]
		size = len(buf)
	case 4:
		if size < 16 {
			return false, buf
		}
		copy(msg.dst_addr[:16], buf[:16])
		buf = buf[16:]
		size = len(buf)
	case 3:
		if size < 1 {
			return false, buf
		}
		copy(msg.dst_addr[:1], buf[:1])
		buf = buf[1:]
		size = len(buf)
		if size < int(msg.dst_addr[0]) {
			return false, buf
		}
		copy(msg.dst_addr[1:], buf[:int(msg.dst_addr[0])])
		buf = buf[int(msg.dst_addr[0]):]
		size = len(buf)
	}
	if size < 2 {
		return false, buf
	}
	copy(msg.dst_port[:], buf[:2])
	msg.dst_port2 = (uint16(msg.dst_port[0]) << 8) + uint16(msg.dst_port[1])

	switch msg.cmd {
	case 1:
		msg.reqtype = "tcp"
	case 2:
		log.Println("BIND")
	case 3:
		msg.reqtype = "udp"
	}
	switch msg.atyp {
	case 1: // ipv4
		msg.url = fmt.Sprintf("%d.%d.%d.%d:%d", msg.dst_addr[0], msg.dst_addr[1], msg.dst_addr[2], msg.dst_addr[3], msg.dst_port2)
	case 3: //DOMANNAME
		msg.url = string(msg.dst_addr[1 : 1+msg.dst_addr[0]])
		msg.url += fmt.Sprintf(":%d", msg.dst_port2)
	case 4: //ipv6
		log.Println("IPV6")
	}
	log.Println(msg.reqtype, msg.url, msg.atyp, msg.dst_port2)
	return true, buf[2:]
}

type Client struct {
	id        string
	buster    bool
	engine    *nat.AttemptEngine
	pipes     map[int]net.Conn          // client for pipes
	specPipes map[string]net.Conn       // client for pipes
	sessions  map[string]*clientSession // session to pipeid
	ready     bool
	bUdp      bool
}

// pipe : client to client
// local : client to local apps
func (sc *Client) getSession(sessionId string) *clientSession {
	session, _ := sc.sessions[sessionId]
	return session
}

func (sc *Client) removeSession(sessionId string) bool {
	if clientType == 1 {
		common.RmId("udp", sessionId)
	}
	session, bHave := sc.sessions[sessionId]
	if bHave {
		if session.localConn != nil {
			session.localConn.Close()
		}
		delete(sc.sessions, sessionId)
		log.Println("client", sc.id, "remove session", sessionId)
		return true
	}
	return false
}

func (sc *Client) OnTunnelRecv(pipe net.Conn, sessionId string, action string, content string) {
	//println("recv p2p tunnel", sessionId, action, content)
	session := sc.getSession(sessionId)
	var conn net.Conn
	if session != nil {
		conn = session.localConn
	}
	switch action {
	case "tunnel_error":
		if conn != nil {
			conn.Write([]byte(content))
			log.Println("tunnel error", content, sessionId)
		}
		sc.removeSession(sessionId)
	//case "serve_begin":
	case "tunnel_msg_s":
		if conn != nil {
			//println("tunnel msg", sessionId, len(content))
			conn.Write([]byte(content))
		} else {
			log.Println("cannot tunnel msg", sessionId)
		}
	case "tunnel_close_s":
		sc.removeSession(sessionId)
	case "tunnel_msg_c":
		if conn != nil {
			//log.Println("tunnel", len(content), sessionId)
			conn.Write([]byte(content))
		} else if *localAddr == "socks5" {
			if session == nil {
				return
			}
			session.processSockProxy(sc, sessionId, content, func() {
				sc.OnTunnelRecv(pipe, sessionId, action, session.recvMsg)
			})
		}
	case "tunnel_close":
		sc.removeSession(sessionId)
	case "tunnel_open":
		if clientType == 0 {
			if *localAddr != "socks5" {
				s_conn, err := net.DialTimeout("tcp", *localAddr, 10*time.Second)
				if err != nil {
					log.Println("connect to local server fail:", err.Error())
					msg := "cannot connect to bind addr" + *localAddr
					common.Write(pipe, sessionId, "tunnel_error", msg)
					//remoteConn.Close()
					return
				} else {
					sc.sessions[sessionId] = &clientSession{pipe: pipe, localConn: s_conn}
					go handleLocalPortResponse(sc, sessionId)
				}
			} else {
				sc.sessions[sessionId] = &clientSession{pipe: pipe, localConn: nil, status: "init", recvMsg: ""}
			}
		}
	}
}

func (sc *Client) Quit() {
	log.Println("client quit", sc.id)
	delete(g_ClientMap, sc.id)
	for id, _ := range sc.sessions {
		sc.removeSession(id)
	}
	for _, pipe := range sc.pipes {
		if pipe != remoteConn {
			pipe.Close()
		}
	}
	if sc.engine != nil {
		sc.engine.Fail()
	}
}

///////////////////////multi pipe support
var g_LocalConn net.Conn

func (sc *Client) MultiListen() bool {
	if g_LocalConn == nil {
		g_LocalConn, err := net.Listen("tcp", *localAddr)
		if err != nil {
			log.Println("cannot listen addr:" + err.Error())
			if remoteConn != nil {
				remoteConn.Close()
			}
			return false
		}
		go func() {
			for {
				conn, err := g_LocalConn.Accept()
				if err != nil {
					continue
				}
				sessionId := common.GetId("udp")
				pipe := sc.getOnePipe()
				if pipe == nil {
					log.Println("cannot get pipe for client")
					if remoteConn != nil {
						remoteConn.Close()
					}
					return
				}
				sc.sessions[sessionId] = &clientSession{pipe: pipe, localConn: conn}
				log.Println("client", sc.id, "create session", sessionId)
				go handleLocalServerResponse(sc, sessionId)
			}
		}()
		mode := "p2p mode"
		if !sc.bUdp {
			mode = "c/s mode"
		}
		println("service start success,please connect", *localAddr, mode)
	}
	return true
}

func (sc *Client) getOnePipe() net.Conn {
	tmp := []int{}
	for id, _ := range sc.pipes {
		tmp = append(tmp, id)
	}
	size := len(tmp)
	if size == 0 {
		return nil
	}
	index := rand.Intn(size)
	log.Println("choose pipe for ", sc.id, ",", index, "of", size)
	hitId := tmp[index]
	pipe, _ := sc.pipes[hitId]
	return pipe
}

///////////////////////multi pipe support

func (sc *Client) Run(index int, specPipe string) {
	var pipe net.Conn
	if index >= 0 {
		pipe = sc.pipes[index]
	} else {
		pipe = sc.specPipes[specPipe]
	}
	if pipe == nil {
		return
	}
	go func() {
		callback := func(conn net.Conn, sessionId, action, content string) {
			if sc != nil {
				sc.OnTunnelRecv(conn, sessionId, action, content)
			}
		}
		common.Read(pipe, callback)
		log.Println("client end read", index)
		if index >= 0 {
			delete(sc.pipes, index)
			if clientType == 1 {
				if len(sc.pipes) == 0 {
					if remoteConn != nil {
						remoteConn.Close()
					}
				}
			}
		} else {
			delete(sc.specPipes, specPipe)
		}
	}()
}

func (sc *Client) LocalAddr() net.Addr                { return nil }
func (sc *Client) Close() error                       { return nil }
func (sc *Client) RemoteAddr() net.Addr               { return nil }
func (sc *Client) SetDeadline(t time.Time) error      { return nil }
func (sc *Client) SetReadDeadline(t time.Time) error  { return nil }
func (sc *Client) SetWriteDeadline(t time.Time) error { return nil }

func handleLocalPortResponse(client *Client, id string) {
	sessionId := id
	if !client.bUdp {
		arr := strings.Split(id, "-")
		sessionId = arr[1]
	}
	session := client.getSession(sessionId)
	if session == nil {
		return
	}
	conn := session.localConn
	if conn == nil {
		return
	}
	arr := make([]byte, 1000)
	reader := bufio.NewReader(conn)
	for {
		size, err := reader.Read(arr)
		if err != nil {
			break
		}
		if common.Write(session.pipe, id, "tunnel_msg_s", string(arr[0:size])) != nil {
			break
		}
	}
	// log.Println("handlerlocal down")
	if client.removeSession(sessionId) {
		common.Write(session.pipe, id, "tunnel_close_s", "")
	}
}

func handleLocalServerResponse(client *Client, sessionId string) {
	session := client.getSession(sessionId)
	if session == nil {
		return
	}
	pipe := session.pipe
	if pipe == nil {
		return
	}
	conn := session.localConn
	common.Write(pipe, sessionId, "tunnel_open", "")
	arr := make([]byte, 1000)
	reader := bufio.NewReader(conn)
	for {
		size, err := reader.Read(arr)
		if err != nil {
			break
		}
		if common.Write(pipe, sessionId, "tunnel_msg_c", string(arr[0:size])) != nil {
			break
		}
	}
	common.Write(pipe, sessionId, "tunnel_close", "")
	client.removeSession(sessionId)
}