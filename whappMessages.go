package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"whapp-irc/maps"
	"whapp-irc/types"
	"whapp-irc/util"
	"whapp-irc/whapp"
)

func formatContact(contact whapp.Contact) types.Participant {
	return types.Participant{
		ID:      contact.ID,
		Contact: contact,
	}
}

func getMessageBody(msg whapp.Message, participants []types.Participant, me whapp.Me) string {
	whappParticipants := make([]whapp.Participant, len(participants))
	for i, p := range participants {
		whappParticipants[i] = whapp.Participant(p)
	}

	switch {
	case msg.Location != nil:
		return maps.ByProvider(
			conf.MapProvider,
			msg.Location.Latitude,
			msg.Location.Longitude,
		)

	case msg.IsMMS:
		res := "--file--"
		if f, has := fs.GetFileByHash(msg.MediaFileHash); has {
			res = f.URL
		}

		if msg.Caption != "" {
			res += " " + msg.FormatCaption(whappParticipants, me.Pushname)
		}

		return res

	default:
		return msg.FormatBody(whappParticipants, me.Pushname)
	}
}

func downloadAndStoreMedia(msg whapp.Message) error {
	if !msg.IsMMS {
		return nil
	}

	if _, has := fs.GetFileByHash(msg.MediaFileHash); !has {
		bytes, err := msg.DownloadMedia()
		if err != nil {
			return err
		}

		ext := util.GetExtensionByMimeOrBytes(msg.MimeType, bytes)
		if ext == "" {
			ext = filepath.Ext(msg.MediaFilename)
			if ext != "" {
				ext = ext[1:]
			}
		}

		if _, err := fs.AddBlob(
			msg.MediaFileHash,
			ext,
			bytes,
		); err != nil {
			return err
		}
	}

	return nil
}

func (conn *Connection) handleWhappMessage(ctx context.Context, msg whapp.Message, fn MessageHandler) error {
	// HACK
	if msg.Type == "e2e_notification" {
		return nil
	}

	item, has := conn.Chats.ByID(msg.Chat.ID, false)
	if !has {
		participants, err := msg.Chat.Participants(ctx, conn.WI)
		if err != nil {
			return err
		}
		item = conn.addChat(conn.convertChat(msg.Chat, participants))
	}
	chat := item.Chat

	if chat.IsGroupChat && !chat.Joined {
		if err := conn.joinChat(item); err != nil {
			return err
		}
	}

	if chat.HasMessageID(msg.ID.Serialized) {
		return nil // already handled
	}
	chat.AddMessageID(msg.ID.Serialized)

	lastTimestamp, found := conn.timestampMap.Get(chat.ID)
	if !found || msg.Timestamp > lastTimestamp {
		conn.timestampMap.Set(chat.ID, msg.Timestamp)
		go conn.saveDatabaseEntry()
	}

	if msg.IsSentByMeFromWeb {
		return nil
	} else if msg.IsNotification {
		return conn.handleWhappNotification(item, msg)
	}

	sender := formatContact(*msg.Sender)
	from := sender.SafeName()
	if msg.IsSentByMe {
		from = conn.irc.Nick()
	}

	var to string
	if chat.IsGroupChat || msg.IsSentByMe {
		to = item.Identifier
	} else {
		to = conn.irc.Nick()
	}

	if err := downloadAndStoreMedia(msg); err != nil {
		return err
	}

	if msg.QuotedMessage != nil {
		body := getMessageBody(*msg.QuotedMessage, chat.Participants, conn.me)
		message := Message{from, to, body, true, &msg}
		if err := fn(conn, message); err != nil {
			return err
		}
	}

	body := getMessageBody(msg, chat.Participants, conn.me)
	return fn(conn, Message{from, to, body, false, &msg})
}

func (conn *Connection) handleWhappNotification(chatItem types.ChatListItem, msg whapp.Message) error {
	chat := chatItem.Chat

	if msg.Type != "gp2" && msg.Type != "call_log" {
		return fmt.Errorf("no idea what to do with notification type %s", msg.Type)
	} else if len(msg.RecipientIDs) == 0 {
		return nil
	}

	findName := func(id whapp.ID) string {
		for _, p := range chat.Participants {
			if p.ID == id {
				return p.SafeName()
			}
		}

		if info, has := conn.Chats.ByID(id, false); has && !info.Chat.IsGroupChat {
			return info.Identifier
		}
		return id.User
	}

	if msg.Sender != nil {
		msg.From = msg.Sender.ID
	}

	var author string
	if msg.From == conn.me.SelfID {
		author = conn.irc.Nick()
	} else {
		author = findName(msg.From)
	}

	for _, recipientID := range msg.RecipientIDs {
		recipientSelf := recipientID == conn.me.SelfID
		var recipient string
		if recipientSelf {
			recipient = conn.irc.Nick()
		} else {
			recipient = findName(recipientID)
		}

		switch msg.Subtype {
		case "create":
			break

		case "add", "invite":
			if recipientSelf {
				// We already handle the new chat JOIN in
				// `Connection::handleWhappMessage` in a better way.
				// So just skip this, since otherwise we JOIN double.
				break
			}
			str := fmt.Sprintf(":%s JOIN %s", recipient, chatItem.Identifier)
			if err := conn.irc.Write(msg.Time(), str); err != nil {
				return err
			}

		case "leave":
			str := fmt.Sprintf(":%s PART %s", recipient, chatItem.Identifier)
			if err := conn.irc.Write(msg.Time(), str); err != nil {
				return err
			}

		case "remove":
			str := fmt.Sprintf(":%s KICK %s %s", author, chatItem.Identifier, recipient)
			if err := conn.irc.Write(msg.Time(), str); err != nil {
				return err
			}

		case "miss":
			if err := conn.irc.PrivateMessage(
				msg.Time(),
				author,
				chatItem.Identifier,
				"-- missed call --",
			); err != nil {
				return err
			}

		default:
			log.Printf("no idea what to do with notification subtype %s\n", msg.Subtype)
		}

		if recipientSelf && (msg.Subtype == "leave" || msg.Subtype == "remove") {
			chat.Joined = false
		}
	}

	return nil
}
