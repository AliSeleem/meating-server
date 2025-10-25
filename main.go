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
	Payload json.RawMessage `json:"payload"`
	Room    string          `json:"room"`
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
	log.Println("Server started on :8080")
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
			if client.room != nil {
				client.room.forwardToTarget(msg.Target, Message{
					Type:    msg.Type,
					Payload: msg.Payload,
					From:    client.id,
					Name:    client.name,
				})
			}
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
	c.room = room

	room.mutex.Lock()
	defer room.mutex.Unlock()

	if len(room.clients) >= maxUsers {
		c.sendMessage(Message{Type: "error", Payload: []byte(`"Room is full"`)})
		return
	}

	room.clients[c] = true
	log.Printf("%s joined room %s", c.id, msg.Room)

	// Send list of existing participants to the new client
	var participants []string
	for cl := range room.clients {
		if cl != c {
			participants = append(participants, cl.id)
		}
	}
	participantsJSON, _ := json.Marshal(participants)
	c.sendMessage(Message{Type: "participants", Payload: participantsJSON})

	// Notify others that this client joined
	room.broadcast(c, Message{
		Type: "user-joined",
		From: c.id,
		Name: c.name,
	})
}

func (c *Client) writeMessages() {
	for msg := range c.send {
		err := c.conn.WriteMessage(websocket.TextMessage, msg)
		if err != nil {
			log.Println("Write error:", err)
			c.leaveRoom()
			return
		}
	}
}

func (c *Client) sendMessage(msg Message) {
	data, _ := json.Marshal(msg)
	c.send <- data
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
		log.Printf("%s left room %s", c.id, room.id)
		room.broadcast(c, Message{Type: "user-left", From: c.id})
	}
	room.mutex.Unlock()

	c.conn.Close()

	// Delete empty room
	if len(room.clients) == 0 {
		roomsMutex.Lock()
		delete(rooms, room.id)
		roomsMutex.Unlock()
		log.Println("Room", room.id, "deleted (empty)")
	}
}

func getOrCreateRoom(roomID string) *Room {
	roomsMutex.Lock()
	defer roomsMutex.Unlock()

	if room, exists := rooms[roomID]; exists {
		return room
	}

	if len(rooms) >= maxRooms {
		return nil
	}

	room := &Room{id: roomID, clients: make(map[*Client]bool)}
	rooms[roomID] = room
	return room
}

func (r *Room) broadcast(sender *Client, msg Message) {
	data, _ := json.Marshal(msg)
	for client := range r.clients {
		if client != sender {
			select {
			case client.send <- data:
			default:
				close(client.send)
				delete(r.clients, client)
			}
		}
	}
}

func (r *Room) forwardToTarget(target string, msg Message) {
	data, _ := json.Marshal(msg)
	for client := range r.clients {
		if client.id == target {
			client.send <- data
			return
		}
	}
}
