package wechat

import "encoding/xml"

// InboundMsg represents a WeChat inbound XML message.
// Different message types populate different fields.
type InboundMsg struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	MsgId        int64    `xml:"MsgId"`

	// Text
	Content string `xml:"Content"`

	// Image
	PicUrl  string `xml:"PicUrl"`
	MediaId string `xml:"MediaId"`

	// Voice
	Format      string `xml:"Format"`
	Recognition string `xml:"Recognition"`

	// Video / ShortVideo
	ThumbMediaId string `xml:"ThumbMediaId"`

	// Link
	Title       string `xml:"Title"`
	Description string `xml:"Description"`
	Url         string `xml:"Url"`

	// Event
	Event    string `xml:"Event"`
	EventKey string `xml:"EventKey"`
}

// EncryptedMsg represents a WeChat encrypted XML envelope.
type EncryptedMsg struct {
	XMLName    xml.Name `xml:"xml"`
	ToUserName string   `xml:"ToUserName"`
	Encrypt    string   `xml:"Encrypt"`
}

// CustomerServiceMsg is the JSON payload for WeChat Customer Service API.
type CustomerServiceMsg struct {
	ToUser  string      `json:"touser"`
	MsgType string      `json:"msgtype"`
	Text    *TextMsg    `json:"text,omitempty"`
	Image   *MediaMsg   `json:"image,omitempty"`
	Voice   *MediaMsg   `json:"voice,omitempty"`
	Video   *VideoMsg   `json:"video,omitempty"`
}

// TextMsg is the text content for Customer Service API.
type TextMsg struct {
	Content string `json:"content"`
}

// MediaMsg is used for image/voice in Customer Service API.
type MediaMsg struct {
	MediaId string `json:"media_id"`
}

// VideoMsg is the video content for Customer Service API.
type VideoMsg struct {
	MediaId      string `json:"media_id"`
	ThumbMediaId string `json:"thumb_media_id"`
	Title        string `json:"title"`
	Description  string `json:"description"`
}
