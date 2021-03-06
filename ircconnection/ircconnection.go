package ircconnection

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"
	"whapp-irc/capabilities"
	"whapp-irc/util"

	"github.com/olebedev/emitter"
	irc "gopkg.in/sorcix/irc.v2"
)

const queueSize = 10

// Connection represents an IRC connection.
type Connection struct {
	Caps *capabilities.Map

	receiveCh chan *irc.Message
	passCh    chan interface{}

	ctx     context.Context
	emitter *emitter.Emitter

	nick string
	pass string

	irc *irc.Conn
}

// HandleConnection wraps around the given socket connection, which you
// shouldn't use after providing it.  It will then handle all the IRC connection
// stuff for you.  You should interface with it using it's methods.
func HandleConnection(ctx context.Context, socket *net.TCPConn) *Connection {
	ctx, cancel := context.WithCancel(ctx)
	conn := &Connection{
		Caps: capabilities.MakeMap(),

		receiveCh: make(chan *irc.Message, queueSize),
		passCh:    make(chan interface{}),

		ctx:     ctx,
		emitter: &emitter.Emitter{},

		irc: irc.NewConn(socket),
	}

	// close irc connection when context ends
	go func() {
		<-ctx.Done()
		conn.irc.Close()
	}()

	// listen for and parse messages.
	// this function also handles IRC commands which are independent of the rest of
	// whapp-irc, such as PINGs.
	go func() {
		defer close(conn.receiveCh)
		defer cancel()

		var passOnce sync.Once

		for {
			msg, err := conn.irc.Decode()
			if err == io.EOF { // connection closed
				return
			} else if err != nil { // socket error
				log.Printf("error while listening for IRC messages: %s\n", err)
				return
			} else if msg == nil { // invalid message
				log.Println("got invalid IRC message, ignoring")
				continue
			}

			switch msg.Command {
			case "PING":
				str := ":whapp-irc PONG whapp-irc :" + msg.Params[0]
				if err := conn.WriteNow(str); err != nil {
					log.Printf("error while sending PONG: %s", err)
					return
				}
			case "QUIT":
				log.Printf("received QUIT from %s", conn.nick)
				return

			case "NICK":
				conn.setNick(msg.Params[0])

			case "PASS":
				conn.pass = ""
				if len(msg.Params) > 0 {
					conn.pass = msg.Params[0]
				}
				passOnce.Do(func() { close(conn.passCh) })

			case "CAP":
				conn.Caps.StartNegotiation()
				switch msg.Params[0] {
				case "LS":
					conn.WriteNow(":whapp-irc CAP * LS :server-time whapp-irc/replay")

				case "LIST":
					caps := conn.Caps.List()
					conn.WriteNow(":whapp-irc CAP * LIST :" + strings.Join(caps, " "))

				case "REQ":
					for _, c := range strings.Split(msg.Trailing(), " ") {
						conn.Caps.Add(c)
					}
					caps := conn.Caps.List()
					conn.WriteNow(":whapp-irc CAP * ACK :" + strings.Join(caps, " "))

				case "END":
					conn.Caps.FinishNegotiation()
				}

			default:
				conn.receiveCh <- msg
			}
		}
	}()

	return conn
}

func write(w io.Writer, msg string) error {
	_, err := w.Write([]byte(msg + "\n"))
	return err
}

// Write writes the given message with the given timestamp to the connection
func (conn *Connection) Write(time time.Time, msg string) error {
	if conn.Caps.Has("server-time") {
		timeFormat := time.UTC().Format("2006-01-02T15:04:05.000Z")
		msg = fmt.Sprintf("@time=%s %s", timeFormat, msg)
	}

	if err := write(conn.irc, msg); err != nil {
		log.Printf("error sending irc message: %s", err)
		return err
	}

	return nil
}

// WriteNow writes the given message with a timestamp of now to the connection.
func (conn *Connection) WriteNow(msg string) error {
	return conn.Write(time.Now(), msg)
}

// WriteListNow writes the given messages with a timestamp of now to the
// connection.
func (conn *Connection) WriteListNow(messages []string) error {
	for _, msg := range messages {
		if err := conn.WriteNow(msg); err != nil {
			return err
		}
	}
	return nil
}

// PrivateMessage sends the given line as a private message from from, to to, on
// the the given date.
func (conn *Connection) PrivateMessage(date time.Time, from, to, line string) error {
	util.LogMessage(date, from, to, line)
	msg := formatPrivateMessage(from, to, line)
	return conn.Write(date, msg)
}

// Status writes the given message as if sent by 'status' to the current
// connection.
func (conn *Connection) Status(body string) error {
	return conn.PrivateMessage(time.Now(), "status", conn.nick, body)
}

// setNick sets the current connection's nickname to the given new nick, and
// notifies any listeners.
func (conn *Connection) setNick(nick string) {
	conn.nick = nick
	<-conn.emitter.Emit("nick", nick)
}

// Nick returns the nickname of the user at the other end of the current
// connection.
func (conn *Connection) Nick() string {
	return conn.nick
}

// Pass returns the password of the user at the other end of the current
// connection.
func (conn *Connection) Pass() string {
	return conn.pass
}

// ReceiveChannel returns the channel where new messages are sent on.
func (conn *Connection) ReceiveChannel() <-chan *irc.Message {
	return conn.receiveCh
}

// NickSetChannel returns a channel that fires when the nickname is changed.
func (conn *Connection) NickSetChannel() <-chan emitter.Event {
	// REVIEW: should this be `On`?
	return conn.emitter.Once("nick")
}

// PassSetChannel returns a channel that closes when the password is set,
// nothing is sent over the channel.
func (conn *Connection) PassSetChannel() <-chan interface{} {
	return conn.passCh
}

// StopChannel returns a channel that closes when the current connection is
// being shut down. No messages are sent over this channel.
func (conn *Connection) StopChannel() <-chan struct{} {
	return conn.ctx.Done()
}
