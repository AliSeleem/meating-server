package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	MaxUsersPerRoom = 4
	MaxRooms        = 50
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// ----------- DATA STRUCTURES -----------

type Client struct {
	id   string
	name string
	conn *websocket.Conn
	send chan []byte
	room *Room
}

type Room struct {
	id      string
	clients map[string]*Client
	mutex   sync.Mutex
}

type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	Room    string          `json:"room"`
	Target  string          `json:"target,omitempty"`
	From    string          `json:"from,omitempty"`
	Name    string          `json:"name,omitempty"`
}

// ----------- GLOBAL STATE -----------

var rooms = make(map[string]*Room)
var roomsMutex sync.Mutex

// ----------- MAIN FUNCTION -----------

func main() {
	http.HandleFunc("/ws", handleWebSocket)
	http.Handle("/", http.FileServer(http.Dir("./client/browser")))
	log.Println("🚀 Server started on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ----------- HANDLERS -----------

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("❌ Upgrade error:", err)
		return
	}

	client := &Client{
		conn: conn,
		send: make(chan []byte, 256),
	}

	go client.writeMessages()
	client.readMessages()
}

func (c *Client) readMessages() {
	defer func() {
		c.leaveRoom()
		c.conn.Close()
	}()

	for {
		var msg Message
		err := c.conn.ReadJSON(&msg)
		if err != nil {
			log.Println("❌ Read error:", err)
			break
		}

		switch msg.Type {
		case "join":
			c.name = msg.Name
			clientID := fmt.Sprintf("%s_%d", msg.Name, time.Now().UnixNano())
			c.id = clientID
			c.joinRoom(msg.Room)

		case "offer", "answer", "ice-candidate":
			if c.room != nil {
				c.room.forwardToTarget(msg.Target, Message{
					Type:    msg.Type,
					Payload: msg.Payload,
					From:    c.id,
					Name:    c.name,
				})
			}
		}
	}
}

// ----------- CLIENT METHODS -----------

func (c *Client) writeMessages() {
	for msg := range c.send {
		err := c.conn.WriteMessage(websocket.TextMessage, msg)
		if err != nil {
			log.Println("❌ Write error:", err)
			break
		}
	}
}

func (c *Client) sendMessage(msg Message) {
	data, _ := json.Marshal(msg)
	c.send <- data
}

func (c *Client) joinRoom(roomID string) {
	room, err := getOrCreateRoom(roomID)
	if err != nil {
		c.sendError(err.Error())
		c.conn.Close()
		return
	}

	room.mutex.Lock()
	defer room.mutex.Unlock()

	if len(room.clients) >= MaxUsersPerRoom {
		c.sendError("Room is full (max 4 users)")
		c.conn.Close()
		return
	}

	room.clients[c.id] = c
	c.room = room

	// Send back confirmation and own ID
	c.sendMessage(Message{Type: "id", Payload: json.RawMessage(`"` + c.id + `"`)})
	c.sendMessage(Message{Type: "joined", Name: c.name, From: c.id, Room: roomID})

	// Notify others
	room.broadcast(c.id, Message{Type: "user-joined", From: c.id, Name: c.name})
	log.Printf("✅ %s joined room %s", c.name, roomID)
}

func (c *Client) leaveRoom() {
	if c.room == nil {
		return
	}

	c.room.mutex.Lock()
	defer c.room.mutex.Unlock()

	delete(c.room.clients, c.id)
	c.room.broadcast(c.id, Message{Type: "user-left", From: c.id, Name: c.name})
	log.Printf("👋 %s left room %s", c.name, c.room.id)

	// If room empty → delete it
	if len(c.room.clients) == 0 {
		deleteRoom(c.room.id)
	}
}

func (c *Client) sendError(message string) {
	errMsg := Message{Type: "error", Payload: json.RawMessage(`"` + message + `"`) }
	data, _ := json.Marshal(errMsg)
	c.conn.WriteMessage(websocket.TextMessage, data)
}

// ----------- ROOM FUNCTIONS -----------

func getOrCreateRoom(roomID string) (*Room, error) {
	roomsMutex.Lock()
	defer roomsMutex.Unlock()

	if len(rooms) >= MaxRooms {
		return nil, fmt.Errorf("Server reached maximum room capacity (50 rooms)")
	}

	room, exists := rooms[roomID]
	if !exists {
		room = &Room{
			id:      roomID,
			clients: make(map[string]*Client),
		}
		rooms[roomID] = room
		log.Printf("🆕 Created room %s (total rooms: %d)", roomID, len(rooms))
	}

	return room, nil
}

func deleteRoom(roomID string) {
	roomsMutex.Lock()
	defer roomsMutex.Unlock()
	delete(rooms, roomID)
	log.Printf("🗑️ Deleted empty room %s", roomID)
}

func (r *Room) broadcast(senderID string, msg Message) {
	data, _ := json.Marshal(msg)
	for id, client := range r.clients {
		if id != senderID {
			client.send <- data
		}
	}
}

func (r *Room) forwardToTarget(targetID string, msg Message) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if targetClient, exists := r.clients[targetID]; exists {
		data, _ := json.Marshal(msg)
		targetClient.send <- data
	}
}
