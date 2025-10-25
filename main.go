package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type Client struct {
	id   string
	name string
	conn *websocket.Conn
	send chan []byte
	room *Room
}

type Room struct {
	id      string
	clients map[*Client]bool
	mutex   sync.Mutex
}

type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Room    string          `json:"room,omitempty"`
	From    string          `json:"from,omitempty"`
	Target  string          `json:"target,omitempty"`
	Name    string          `json:"name,omitempty"`
}

var (
	rooms      = make(map[string]*Room)
	roomsMutex sync.Mutex
	maxRooms   = 50
	maxUsers   = 4
)

func main() {
	http.HandleFunc("/ws", handleWebSocket)
	http.Handle("/", http.FileServer(http.Dir("./client/browser")))
	log.Println("✅ Server started on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}
	client := &Client{conn: conn, send: make(chan []byte, 256)}

	go client.writeMessages()

	for {
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Println("Read error:", err)
			client.leaveRoom()
			break
		}

		switch msg.Type {
		case "join":
			client.handleJoin(msg)
		case "offer", "answer", "ice-candidate":
			client.forwardToTarget(msg)
		case "leave":
			client.leaveRoom()
		}
	}
}

func (c *Client) handleJoin(msg Message) {
	var joinData struct {
		Name string `json:"name"`
	}
	json.Unmarshal(msg.Payload, &joinData)
	c.name = joinData.Name
	c.id = c.name + "_" + time.Now().Format("150405")

	room := getOrCreateRoom(msg.Room)
	if room == nil {
		c.sendMessage(Message{Type: "error", Payload: []byte(`"Max rooms limit reached"`)})
		return
	}
	c.room = room

	room.mutex.Lock()
	defer room.mutex.Unlock()

	if len(room.clients) >= maxUsers {
		c.sendMessage(Message{Type: "error", Payload: []byte(`"Room is full"`)})
		return
	}

	room.clients[c] = true
	log.Printf("%s joined room %s", c.id, msg.Room)

	// send list of participants to new user
	var participants []map[string]string
	for cl := range room.clients {
		if cl != c {
			participants = append(participants, map[string]string{
				"id":   cl.id,
				"name": cl.name,
			})
		}
	}
	listJSON, _ := json.Marshal(participants)
	c.sendMessage(Message{Type: "participants", Payload: listJSON})

	// notify others
	room.broadcast(c, Message{Type: "user-joined", From: c.id, Name: c.name})
}

func (c *Client) writeMessages() {
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			c.leaveRoom()
			return
		}
	}
}

func (c *Client) sendMessage(msg Message) {
	data, _ := json.Marshal(msg)
	c.send <- data
}

func (c *Client) forwardToTarget(msg Message) {
	if c.room == nil {
		return
	}
	c.room.mutex.Lock()
	defer c.room.mutex.Unlock()
	for client := range c.room.clients {
		if client.id == msg.Target {
			msg.From = c.id
			data, _ := json.Marshal(msg)
			client.send <- data
			break
		}
	}
}

func (c *Client) leaveRoom() {
	if c.room == nil {
		return
	}
	room := c.room
	room.mutex.Lock()
	if _, exists := room.clients[c]; exists {
		delete(room.clients, c)
		close(c.send)
		room.broadcast(c, Message{Type: "user-left", From: c.id})
	}
	room.mutex.Unlock()
	c.conn.Close()

	// delete empty room
	if len(room.clients) == 0 {
		roomsMutex.Lock()
		delete(rooms, room.id)
		roomsMutex.Unlock()
		log.Println("Room", room.id, "deleted (empty)")
	}
}

func (r *Room) broadcast(sender *Client, msg Message) {
	data, _ := json.Marshal(msg)
	for client := range r.clients {
		if client != sender {
			client.send <- data
		}
	}
}

func getOrCreateRoom(id string) *Room {
	roomsMutex.Lock()
	defer roomsMutex.Unlock()
	if room, ok := rooms[id]; ok {
		return room
	}
	if len(rooms) >= maxRooms {
		return nil
	}
	room := &Room{id: id, clients: make(map[*Client]bool)}
	rooms[id] = room
	return room
}
