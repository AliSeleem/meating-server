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

// ====== Structs ======
type Client struct {
	ID     string
	Name   string
	RoomID string
	conn   *websocket.Conn
	send   chan []byte
}

type Room struct {
	ID      string
	Clients map[string]*Client
	mutex   sync.Mutex
}

type Message struct {
	Type    string          `json:"type"`
	RoomID  string          `json:"room"`
	Target  string          `json:"target,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Name    string          `json:"name,omitempty"`
	UserID  string          `json:"userId,omitempty"`
}

var (
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}
	rooms      = make(map[string]*Room)
	roomsMutex sync.Mutex
	maxRooms   = 50
	maxUsers   = 4
)

// ====== MAIN ======
func main() {
	http.HandleFunc("/ws", handleWebSocket)
	http.Handle("/", http.FileServer(http.Dir("./client/browser")))
	log.Println("✅ Signaling server running on :8080 ...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ====== Handlers ======
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("❌ Upgrade error:", err)
		return
	}

	client := &Client{conn: conn, send: make(chan []byte, 256)}
	go client.writePump()

	for {
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Printf("❌ Read error from client: %v\n", err)
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

// ====== Client Handlers ======

func (c *Client) handleJoin(msg Message) {
	name := msg.Name
	roomID := msg.RoomID

	if name == "" || roomID == "" {
		c.sendError("Missing name or room")
		return
	}

	userID := fmt.Sprintf("%s_%d", name, time.Now().UnixMilli())
	c.ID = userID
	c.Name = name
	c.RoomID = roomID

	room := getOrCreateRoom(roomID)
	room.mutex.Lock()
	defer room.mutex.Unlock()

	if len(room.Clients) >= maxUsers {
		c.sendError("Room is full")
		c.conn.Close()
		return
	}

	room.Clients[c.ID] = c

	// Prepare peers list (other clients)
	var peers []map[string]string
	for id, cl := range room.Clients {
		if id != c.ID {
			peers = append(peers, map[string]string{"userId": id, "name": cl.Name})
		}
	}

	// Send joined confirmation
	joinedMsg := Message{
		Type:   "joined",
		UserID: c.ID,
		RoomID: roomID,
		Payload: mustJSON(map[string]interface{}{
			"peers": peers,
		}),
	}
	c.sendJSON(joinedMsg)

	// Notify others
	broadcastExcept(room, c.ID, Message{
		Type:   "user-joined",
		UserID: c.ID,
		Name:   c.Name,
	})
}

func (c *Client) leaveRoom() {
	if c.RoomID == "" {
		return
	}
	roomsMutex.Lock()
	room, exists := rooms[c.RoomID]
	roomsMutex.Unlock()
	if !exists {
		return
	}

	room.mutex.Lock()
	delete(room.Clients, c.ID)
	room.mutex.Unlock()

	broadcastExcept(room, c.ID, Message{
		Type:   "user-left",
		UserID: c.ID,
	})

	c.conn.Close()

	// Clean up empty rooms
	room.mutex.Lock()
	if len(room.Clients) == 0 {
		roomsMutex.Lock()
		delete(rooms, room.ID)
		roomsMutex.Unlock()
		log.Printf("🧹 Room %s deleted (empty)\n", room.ID)
	}
	room.mutex.Unlock()
}

func (c *Client) forwardToTarget(msg Message) {
	if msg.Target == "" {
		c.sendError("Missing target userId")
		return
	}
	roomsMutex.Lock()
	room, exists := rooms[msg.RoomID]
	roomsMutex.Unlock()
	if !exists {
		c.sendError("Room not found")
		return
	}

	room.mutex.Lock()
	target, exists := room.Clients[msg.Target]
	room.mutex.Unlock()
	if !exists {
		c.sendError("Target not found")
		return
	}

	target.sendJSON(Message{
		Type:    msg.Type,
		UserID:  c.ID,
		Payload: msg.Payload,
	})
}

// ====== Utilities ======
func (c *Client) sendError(text string) {
	c.sendJSON(Message{Type: "error", Payload: mustJSON(map[string]string{"message": text})})
}

func (c *Client) sendJSON(msg Message) {
	data, _ := json.Marshal(msg)
	select {
	case c.send <- data:
	default:
		close(c.send)
	}
}

func (c *Client) writePump() {
	for msg := range c.send {
		err := c.conn.WriteMessage(websocket.TextMessage, msg)
		if err != nil {
			log.Println("❌ Write error:", err)
			break
		}
	}
	c.conn.Close()
}

func getOrCreateRoom(roomID string) *Room {
	roomsMutex.Lock()
	defer roomsMutex.Unlock()

	if len(rooms) >= maxRooms {
		log.Println("❌ Room limit reached")
		return &Room{ID: "invalid"}
	}

	if r, exists := rooms[roomID]; exists {
		return r
	}

	r := &Room{
		ID:      roomID,
		Clients: make(map[string]*Client),
	}
	rooms[roomID] = r
	log.Printf("🆕 Room created: %s\n", roomID)
	return r
}

func broadcastExcept(room *Room, senderID string, msg Message) {
	data, _ := json.Marshal(msg)
	for id, client := range room.Clients {
		if id == senderID {
			continue
		}
		select {
		case client.send <- data:
		default:
			close(client.send)
			delete(room.Clients, id)
		}
	}
}

func mustJSON(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
