package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type Client struct {
	conn *websocket.Conn
	send chan []byte
}

type Room struct {
	clients map[*Client]bool
	mutex   sync.Mutex
}

type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	Room    string          `json:"room"`
}

var rooms = make(map[string]*Room)
var roomsMutex sync.Mutex

func main() {
	http.HandleFunc("/ws", handleWebSocket)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}
	defer conn.Close()

	client := &Client{conn: conn, send: make(chan []byte)}
	go client.writeMessages()

	for {
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Println("Read error:", err)
			client.leaveRoom()
			break
		}
		fmt.Println(msg)

		room := getOrCreateRoom(msg.Room)
		room.mutex.Lock()
		if msg.Type == "join" {
			room.clients[client] = true
			broadcastToRoom(room, client, Message{Type: "user-joined"})
		} else {
			broadcastToRoom(room, client, msg)
		}
		room.mutex.Unlock()
	}
}

func (c *Client) writeMessages() {
	for msg := range c.send {
		err := c.conn.WriteMessage(websocket.TextMessage, msg)
		if err != nil {
			log.Println("Write error:", err)
			return
		}
	}
}

func (c *Client) leaveRoom() {
	for _, room := range rooms {
		room.mutex.Lock()
		if _, exists := room.clients[c]; exists {
			delete(room.clients, c)
			broadcastToRoom(room, c, Message{Type: "user-left"})
		}
		room.mutex.Unlock()
	}
}

func getOrCreateRoom(roomID string) *Room {
	roomsMutex.Lock()
	defer roomsMutex.Unlock()
	if room, exists := rooms[roomID]; exists {
		return room
	}
	room := &Room{clients: make(map[*Client]bool)}
	rooms[roomID] = room
	return room
}

func broadcastToRoom(room *Room, sender *Client, msg Message) {
	data, _ := json.Marshal(msg)
	for client := range room.clients {
		if client != sender {
			select {
			case client.send <- data:
			default:
				close(client.send)
				delete(room.clients, client)
			}
		}
	}
}
