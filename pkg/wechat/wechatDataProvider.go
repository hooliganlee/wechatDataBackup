package wechat

import (
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	sync "sync"
	"time"

	"github.com/beevik/etree"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pierrec/lz4"
	"google.golang.org/protobuf/proto"
)

const (
	Wechat_Message_Type_Text    = 1
	Wechat_Message_Type_Picture = 3
	Wechat_Message_Type_Voice   = 34
	Wechat_Message_Type_Video   = 43
	Wechat_Message_Type_Emoji   = 47
	Wechat_Message_Type_Misc    = 49
	Wechat_Message_Type_System  = 10000
)

const (
	Wechat_Misc_Message_TEXT           = 1
	Wechat_Misc_Message_CardLink       = 5
	Wechat_Misc_Message_File           = 6
	Wechat_Misc_Message_CustomEmoji    = 8
	Wechat_Misc_Message_ForwardMessage = 19
	Wechat_Misc_Message_Applet         = 33
	Wechat_Misc_Message_Applet2        = 36
	Wechat_Misc_Message_Refer          = 57
	Wechat_Misc_Message_Live           = 63
	Wechat_Misc_Message_Notice         = 87
	Wechat_Misc_Message_Live2          = 88
	Wechat_Misc_Message_Transfer       = 2000
	Wechat_Misc_Message_RedPacket      = 2003
)

const (
	Wechat_System_Message_Notice  = 1
	Wechat_System_Message_Tickle  = 4
	Wechat_System_Message_Notice2 = 8000
)

type Message_Search_Direction int

const (
	Message_Search_Forward Message_Search_Direction = iota
	Message_Search_Backward
	Message_Search_Both
)

type WeChatUserInfo struct {
	UserName        string `json:"UserName"`
	Alias           string `json:"Alias"`
	ReMark          string `json:"ReMark"`
	NickName        string `json:"NickName"`
	SmallHeadImgUrl string `json:"SmallHeadImgUrl"`
	BigHeadImgUrl   string `json:"BigHeadImgUrl"`
}

type WeChatSession struct {
	UserName string         `json:"UserName"`
	NickName string         `json:"NickName"`
	Content  string         `json:"Content"`
	UserInfo WeChatUserInfo `json:"UserInfo"`
	Time     uint64         `json:"Time"`
	IsGroup  bool           `json:IsGroup`
}

type WeChatSessionList struct {
	Total int             `json:"Total"`
	Rows  []WeChatSession `json:"Rows"`
}

type FileInfo struct {
	FileName string `json:"fileName"`
	FileSize string `json:"fileSize"`
	FilePath string `json:"filePath"`
	FileExt  string `json:"fileExt"`
}

type LinkInfo struct {
	Url         string `json:"Url"`
	Title       string `json:"Title"`
	Description string `json:"Description"`
	DisPlayName string `json:"DisPlayName"`
}

type ReferInfo struct {
	Type        int    `json:"Type"`
	SubType     int    `json:"SubType"`
	Svrid       int64  `json:"Svrid"`
	Displayname string `json:"Displayname"`
	Content     string `json:"Content"`
}

type WeChatMessage struct {
	LocalId         int            `json:"LocalId"`
	MsgSvrId        int64          `json:"MsgSvrId"`
	Type            int            `json:"type"`
	SubType         int            `json:"SubType"`
	IsSender        int            `json:"IsSender"`
	CreateTime      int64          `json:"createTime"`
	Talker          string         `json:"talker"`
	Content         string         `json:"content"`
	ThumbPath       string         `json:"ThumbPath"`
	ImagePath       string         `json:"ImagePath"`
	VideoPath       string         `json:"VideoPath"`
	FileInfo        FileInfo       `json:"fileInfo"`
	EmojiPath       string         `json:"EmojiPath"`
	VoicePath       string         `json:"VoicePath"`
	IsChatRoom      bool           `json:"isChatRoom"`
	UserInfo        WeChatUserInfo `json:"userInfo"`
	LinkInfo        LinkInfo       `json:"LinkInfo"`
	ReferInfo       ReferInfo      `json:"ReferInfo"`
	compressContent []byte
	bytesExtra      []byte
}

type WeChatMessageList struct {
	KeyWord string          `json:"KeyWord"`
	Total   int             `json:"Total"`
	Rows    []WeChatMessage `json:"Rows"`
}

type WeChatMessageDate struct {
	Date  []string `json:"Date"`
	Total int      `json:"Total"`
}

type WeChatUserList struct {
	Users []WeChatUserInfo `json:"Users"`
	Total int              `json:"Total"`
}

type wechatMsgDB struct {
	path      string
	db        *sql.DB
	startTime int64
	endTime   int64
}

type WechatDataProvider struct {
	resPath  string
	microMsg *sql.DB

	msgDBs      []*wechatMsgDB
	userInfoMap map[string]WeChatUserInfo
	userInfoMtx sync.Mutex

	SelfInfo *WeChatUserInfo
}

const (
	MicroMsgDB = "MicroMsg.db"
)

type byTime []*wechatMsgDB

func (a byTime) Len() int           { return len(a) }
func (a byTime) Less(i, j int) bool { return a[i].startTime > a[j].startTime }
func (a byTime) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

func CreateWechatDataProvider(resPath string) (*WechatDataProvider, error) {
	provider := &WechatDataProvider{}
	provider.resPath = resPath
	provider.msgDBs = make([]*wechatMsgDB, 0)
	log.Println(resPath)
	userName := filepath.Base(resPath)
	MicroMsgDBPath := resPath + "\\Msg\\" + MicroMsgDB
	microMsg, err := sql.Open("sqlite3", MicroMsgDBPath)
	if err != nil {
		log.Printf("open db %s error: %v", MicroMsgDBPath, err)
		return provider, err
	}

	index := 0
	for {
		msgDBPath := fmt.Sprintf("%s\\Msg\\Multi\\MSG%d.db", provider.resPath, index)
		if _, err := os.Stat(msgDBPath); err != nil {
			log.Println("msgDBPath end", msgDBPath)
			break
		}

		msgDB, err := wechatOpenMsgDB(msgDBPath)
		if err != nil {
			log.Printf("open db %s error: %v", msgDBPath, err)
			break
		}
		provider.msgDBs = append(provider.msgDBs, msgDB)
		log.Printf("MSG%d.db start %d - %d end\n", index, msgDB.startTime, msgDB.endTime)
		index += 1
	}
	sort.Sort(byTime(provider.msgDBs))
	for _, db := range provider.msgDBs {
		log.Printf("%s start %d - %d end\n", db.path, db.startTime, db.endTime)
	}
	provider.userInfoMap = make(map[string]WeChatUserInfo)
	provider.microMsg = microMsg
	provider.SelfInfo, err = provider.WechatGetUserInfoByName(userName)
	if err != nil {
		log.Printf("WechatGetUserInfoByName %s failed: %v", userName, err)
		return provider, err
	}
	provider.userInfoMap[userName] = *provider.SelfInfo
	log.Println("resPath:", provider.resPath)
	return provider, nil
}

func (P *WechatDataProvider) WechatWechatDataProviderClose() {
	if P.microMsg != nil {
		P.microMsg.Close()
	}

	for _, db := range P.msgDBs {
		db.db.Close()
	}
	log.Println("WechatWechatDataProviderClose")
}

func (P *WechatDataProvider) WechatGetUserInfoByName(name string) (*WeChatUserInfo, error) {
	info := &WeChatUserInfo{}

	var UserName, Alias, ReMark, NickName string
	querySql := fmt.Sprintf("select ifnull(UserName,'') as UserName, ifnull(Alias,'') as Alias, ifnull(ReMark,'') as ReMark, ifnull(NickName,'') as NickName from Contact where UserName='%s';", name)
	// log.Println(querySql)
	err := P.microMsg.QueryRow(querySql).Scan(&UserName, &Alias, &ReMark, &NickName)
	if err != nil {
		log.Println("not found User:", err)
		return info, err
	}

	log.Printf("UserName %s, Alias %s, ReMark %s, NickName %s\n", UserName, Alias, ReMark, NickName)

	var smallHeadImgUrl, bigHeadImgUrl string
	querySql = fmt.Sprintf("select ifnull(smallHeadImgUrl,'') as smallHeadImgUrl, ifnull(bigHeadImgUrl,'') as bigHeadImgUrl from ContactHeadImgUrl where usrName='%s';", UserName)
	// log.Println(querySql)
	err = P.microMsg.QueryRow(querySql).Scan(&smallHeadImgUrl, &bigHeadImgUrl)
	if err != nil {
		log.Println("not find headimg", err)
	}

	info.UserName = UserName
	info.Alias = Alias
	info.ReMark = ReMark
	info.NickName = NickName
	info.SmallHeadImgUrl = smallHeadImgUrl
	info.BigHeadImgUrl = bigHeadImgUrl

	// log.Println(info)
	return info, nil
}

func (P *WechatDataProvider) WeChatGetSessionList(pageIndex int, pageSize int) (*WeChatSessionList, error) {
	List := &WeChatSessionList{}
	List.Rows = make([]WeChatSession, 0)

	querySql := fmt.Sprintf("select ifnull(strUsrName,'') as strUsrName,ifnull(strNickName,'') as strNickName,ifnull(strContent,'') as strContent, nTime from Session order by nOrder desc limit %d, %d;", pageIndex*pageSize, pageSize)
	dbRows, err := P.microMsg.Query(querySql)
	if err != nil {
		log.Println(err)
		return List, err
	}
	defer dbRows.Close()

	var strUsrName, strNickName, strContent string
	var nTime uint64
	for dbRows.Next() {
		var session WeChatSession
		err = dbRows.Scan(&strUsrName, &strNickName, &strContent, &nTime)
		if err != nil {
			log.Println(err)
			continue
		}
		if len(strContent) == 0 {
			log.Printf("%s cotent nil\n", strUsrName)
			continue
		}

		session.UserName = strUsrName
		session.NickName = strNickName
		session.Content = strContent
		session.Time = nTime
		session.IsGroup = strings.HasSuffix(strUsrName, "@chatroom")
		info, err := P.WechatGetUserInfoByName(strUsrName)
		if err != nil {
			log.Printf("WechatGetUserInfoByName %s failed\n", strUsrName)
			continue
		}
		session.UserInfo = *info
		List.Rows = append(List.Rows, session)
		List.Total += 1
	}

	return List, nil
}

func (P *WechatDataProvider) WeChatGetMessageListByTime(userName string, time int64, pageSize int, direction Message_Search_Direction) (*WeChatMessageList, error) {

	List := &WeChatMessageList{}
	List.Rows = make([]WeChatMessage, 0)
	selectTime := time
	selectpageSize := pageSize

	if direction == Message_Search_Both {
		selectpageSize = pageSize / 2
	}
	for direction == Message_Search_Forward || direction == Message_Search_Both {
		selectList, err := P.weChatGetMessageListByTime(userName, selectTime, selectpageSize, Message_Search_Forward)
		if err != nil {
			return List, err
		}

		if selectList.Total == 0 {
			break
		}

		selectTime = selectList.Rows[selectList.Total-1].CreateTime - 1
		selectpageSize -= selectList.Total
		List.Total += selectList.Total
		List.Rows = append(List.Rows, selectList.Rows...)
		if selectpageSize <= 0 {
			break
		}
		log.Printf("Forward selectTime %d, selectpageSize %d\n", selectTime, selectpageSize)
	}

	selectTime = time
	if direction == Message_Search_Both {
		selectpageSize = pageSize / 2
	}
	for direction == Message_Search_Backward || direction == Message_Search_Both {
		selectList, err := P.weChatGetMessageListByTime(userName, selectTime, selectpageSize, Message_Search_Backward)
		if err != nil {
			return List, err
		}

		if selectList.Total == 0 {
			break
		}

		selectTime = selectList.Rows[0].CreateTime + 1
		selectpageSize -= selectList.Total
		List.Total += selectList.Total
		List.Rows = append(selectList.Rows, List.Rows...)
		if selectpageSize <= 0 {
			break
		}
		log.Printf("Backward selectTime %d, selectpageSize %d\n", selectTime, selectpageSize)
	}

	return List, nil
}

func (P *WechatDataProvider) weChatGetMessageListByTime(userName string, time int64, pageSize int, direction Message_Search_Direction) (*WeChatMessageList, error) {
	List := &WeChatMessageList{}
	List.Rows = make([]WeChatMessage, 0)
	index := P.wechatFindDBIndex(userName, time, direction)
	if index == -1 {
		log.Printf("Not found %s %d data\n", userName, time)
		return List, nil
	}

	sqlFormat := "select localId,MsgSvrID,Type,SubType,IsSender,CreateTime,ifnull(StrTalker,'') as StrTalker, ifnull(StrContent,'') as StrContent,ifnull(CompressContent,'') as CompressContent,ifnull(BytesExtra,'') as BytesExtra from MSG Where StrTalker='%s' And CreateTime<=%d order by CreateTime desc limit %d;"
	if direction == Message_Search_Backward {
		sqlFormat = "select localId,MsgSvrID,Type,SubType,IsSender,CreateTime,ifnull(StrTalker,'') as StrTalker, ifnull(StrContent,'') as StrContent,ifnull(CompressContent,'') as CompressContent,ifnull(BytesExtra,'') as BytesExtra from ( select localId, MsgSvrID, Type, SubType, IsSender, CreateTime, StrTalker, StrContent, CompressContent, BytesExtra FROM MSG Where StrTalker='%s' And CreateTime>%d order by CreateTime asc limit %d) AS SubQuery order by CreateTime desc;"
	}
	querySql := fmt.Sprintf(sqlFormat, userName, time, pageSize)
	log.Println(querySql)

	rows, err := P.msgDBs[index].db.Query(querySql)
	if err != nil {
		log.Printf("%s failed %v\n", querySql, err)
		return List, nil
	}
	defer rows.Close()
	var localId, Type, SubType, IsSender int
	var MsgSvrID, CreateTime int64
	var StrTalker, StrContent string
	var CompressContent, BytesExtra []byte

	for rows.Next() {
		message := WeChatMessage{}
		err = rows.Scan(&localId, &MsgSvrID, &Type, &SubType, &IsSender, &CreateTime,
			&StrTalker, &StrContent, &CompressContent, &BytesExtra)
		if err != nil {
			log.Println("rows.Scan failed", err)
			return List, err
		}
		message.LocalId = localId
		message.MsgSvrId = MsgSvrID
		message.Type = Type
		message.SubType = SubType
		message.IsSender = IsSender
		message.CreateTime = CreateTime
		message.Talker = StrTalker
		message.Content = StrContent
		message.IsChatRoom = strings.HasSuffix(StrTalker, "@chatroom")
		message.compressContent = make([]byte, len(CompressContent))
		message.bytesExtra = make([]byte, len(BytesExtra))
		copy(message.compressContent, CompressContent)
		copy(message.bytesExtra, BytesExtra)
		P.wechatMessageExtraHandle(&message)
		P.wechatMessageGetUserInfo(&message)
		P.wechatMessageEmojiHandle(&message)
		P.wechatMessageCompressContentHandle(&message)
		List.Rows = append(List.Rows, message)
		List.Total += 1
	}

	if err := rows.Err(); err != nil {
		log.Println("rows.Scan failed", err)
		return List, err
	}

	return List, nil
}

func (P *WechatDataProvider) WeChatGetMessageListByKeyWord(userName string, time int64, keyWord string, msgType string, pageSize int) (*WeChatMessageList, error) {
	List := &WeChatMessageList{}
	List.Rows = make([]WeChatMessage, 0)
	List.KeyWord = keyWord
	_time := time
	selectPagesize := pageSize
	if keyWord != "" || msgType != "" {
		selectPagesize = 600
	}
	for {
		log.Println("time:", _time, keyWord)
		rawList, err := P.weChatGetMessageListByTime(userName, _time, selectPagesize, Message_Search_Forward)
		if err != nil {
			log.Println("weChatGetMessageListByTime failed: ", err)
			return nil, err
		}
		log.Println("rawList.Total:", rawList.Total)
		if rawList.Total == 0 {
			if List.Total == 0 {
				log.Printf("user %s not find [%s]\n", userName, keyWord)
			}
			break
		}

		for i, _ := range rawList.Rows {
			if weChatMessageTypeFilter(&rawList.Rows[i], msgType) && (len(keyWord) == 0 || weChatMessageContains(&rawList.Rows[i], keyWord)) {
				List.Rows = append(List.Rows, rawList.Rows[i])
				List.Total += 1
				if List.Total >= pageSize {
					return List, nil
				}
			}
		}

		_time = rawList.Rows[rawList.Total-1].CreateTime - 1
	}

	return List, nil
}

func (P *WechatDataProvider) WeChatGetMessageDate(userName string) (*WeChatMessageDate, error) {
	messageData := &WeChatMessageDate{}
	messageData.Date = make([]string, 0)
	messageData.Total = 0

	_time := time.Now().Unix()

	for {
		index := P.wechatFindDBIndex(userName, _time, Message_Search_Forward)
		if index == -1 {
			log.Println("wechat find db end")
			return messageData, nil
		}

		sqlFormat := " SELECT DISTINCT strftime('%%Y-%%m-%%d', datetime(CreateTime+28800, 'unixepoch')) FROM MSG WHERE StrTalker='%s' order by CreateTime desc;"
		querySql := fmt.Sprintf(sqlFormat, userName)

		rows, err := P.msgDBs[index].db.Query(querySql)
		if err != nil {
			log.Printf("%s failed %v\n", querySql, err)
			return messageData, nil
		}
		defer rows.Close()

		var date string
		for rows.Next() {
			err = rows.Scan(&date)
			if err != nil {
				log.Println("rows.Scan failed", err)
				return messageData, err
			}

			messageData.Date = append(messageData.Date, date)
			messageData.Total += 1
		}

		if err := rows.Err(); err != nil {
			log.Println("rows.Scan failed", err)
			return messageData, err
		}

		_time = P.wechatGetLastMessageCreateTime(userName, index)
		if -1 == _time {
			log.Println("wechatGetLastMessageCreateTime failed")
			return messageData, errors.New("wechatGetLastMessageCreateTime failed")
		}

		_time -= 1
	}
}

func (P *WechatDataProvider) WeChatGetChatRoomUserList(chatroom string) (*WeChatUserList, error) {
	userList := &WeChatUserList{}
	userList.Users = make([]WeChatUserInfo, 0)
	userList.Total = 0

	sqlFormat := "select UserNameList from ChatRoom where ChatRoomName='%s';"
	querySql := fmt.Sprintf(sqlFormat, chatroom)

	var userNameListStr string
	err := P.microMsg.QueryRow(querySql).Scan(&userNameListStr)
	if err != nil {
		log.Println("Scan: ", err)
		return nil, err
	}

	userNameArray := strings.Split(userNameListStr, "^G")
	log.Println("userNameArray:", userNameArray)

	for _, userName := range userNameArray {
		pinfo, err := P.WechatGetUserInfoByNameOnCache(userName)
		if err == nil {
			userList.Users = append(userList.Users, *pinfo)
			userList.Total += 1
		}
	}

	return userList, nil
}

func (info WeChatUserInfo) String() string {
	return fmt.Sprintf("NickName:[%s] Alias:[%s], NickName:[%s], ReMark:[%s], SmallHeadImgUrl:[%s], BigHeadImgUrl[%s]",
		info.NickName, info.Alias, info.NickName, info.ReMark, info.SmallHeadImgUrl, info.BigHeadImgUrl)
}

func (P *WechatDataProvider) wechatMessageExtraHandle(msg *WeChatMessage) {
	var extra MessageBytesExtra
	err := proto.Unmarshal(msg.bytesExtra, &extra)
	if err != nil {
		log.Println("proto.Unmarshal failed", err)
		return
	}

	for _, ext := range extra.Message2 {
		switch ext.Field1 {
		case 1:
			if msg.IsChatRoom {
				msg.Talker = ext.Field2
			}
		case 3:
			if len(ext.Field2) > 0 && (msg.Type == Wechat_Message_Type_Picture || msg.Type == Wechat_Message_Type_Video || msg.Type == Wechat_Message_Type_Misc) {
				msg.ThumbPath = P.resPath + ext.Field2[len(P.SelfInfo.UserName):]
			}
		case 4:
			if len(ext.Field2) > 0 {
				if msg.Type == Wechat_Message_Type_Misc && msg.SubType == Wechat_Misc_Message_File {
					msg.FileInfo.FilePath = P.resPath + ext.Field2[len(P.SelfInfo.UserName):]
					msg.FileInfo.FileName = filepath.Base(ext.Field2)
				} else if msg.Type == Wechat_Message_Type_Picture || msg.Type == Wechat_Message_Type_Video || msg.Type == Wechat_Message_Type_Misc {
					msg.ImagePath = P.resPath + ext.Field2[len(P.SelfInfo.UserName):]
					msg.VideoPath = P.resPath + ext.Field2[len(P.SelfInfo.UserName):]
				}
			}
		}
	}

	if msg.Type == Wechat_Message_Type_Voice {
		msg.VoicePath = fmt.Sprintf("%s\\FileStorage\\Voice\\%d.mp3", P.resPath, msg.MsgSvrId)
	}
}

type EmojiMsg struct {
	XMLName xml.Name `xml:"msg"`
	Emoji   Emoji    `xml:"emoji"`
}

type Emoji struct {
	XMLName  xml.Name `xml:"emoji"`
	CdnURL   string   `xml:"cdnurl,attr"`
	Thumburl string   `xml:"thumburl,attr"`
	Width    string   `xml:"width,attr"`
	Height   string   `xml:"height,attr"`
}

func (P *WechatDataProvider) wechatMessageEmojiHandle(msg *WeChatMessage) {
	if msg.Type != Wechat_Message_Type_Emoji {
		return
	}

	emojiMsg := EmojiMsg{}
	err := xml.Unmarshal([]byte(msg.Content), &emojiMsg)
	if err != nil {
		log.Println("xml.Unmarshal failed: ", err, msg.Content)
		return
	}

	msg.EmojiPath = emojiMsg.Emoji.CdnURL
}

type xmlDocument struct {
	*etree.Document
}

func NewxmlDocument(e *etree.Document) *xmlDocument {
	return &xmlDocument{e}
}

func (e *xmlDocument) FindElementValue(path string) string {
	item := e.FindElement(path)
	if item != nil {
		return item.Text()
	}

	return ""
}

func (P *WechatDataProvider) wechatMessageCompressContentHandle(msg *WeChatMessage) {
	if len(msg.compressContent) == 0 {
		return
	}

	unCompressContent := make([]byte, len(msg.compressContent)*10)
	ulen, err := lz4.UncompressBlock(msg.compressContent, unCompressContent)
	if err != nil {
		log.Println("UncompressBlock failed:", err, msg.MsgSvrId)
		return
	}

	compMsg := etree.NewDocument()
	if err := compMsg.ReadFromBytes(unCompressContent[:ulen-1]); err != nil {
		// os.WriteFile("D:\\tmp\\"+string(msg.LocalId)+".xml", unCompressContent[:ulen], 0600)
		log.Println("ReadFromBytes failed:", err)
		return
	}

	root := NewxmlDocument(compMsg)
	if msg.Type == Wechat_Message_Type_Misc && msg.SubType == Wechat_Misc_Message_CardLink {
		msg.LinkInfo.Title = root.FindElementValue("/msg/appmsg/title")
		msg.LinkInfo.Description = root.FindElementValue("/msg/appmsg/des")
		msg.LinkInfo.Url = root.FindElementValue("/msg/appmsg/url")
		msg.LinkInfo.DisPlayName = root.FindElementValue("/msg/appmsg/sourcedisplayname")
		appName := root.FindElementValue("/msg/appinfo/appname")
		if len(msg.LinkInfo.DisPlayName) == 0 && len(appName) > 0 {
			msg.LinkInfo.DisPlayName = appName
		}
	} else if msg.Type == Wechat_Message_Type_Misc && msg.SubType == Wechat_Misc_Message_Refer {
		msg.Content = root.FindElementValue("/msg/appmsg/title")
		msg.ReferInfo.Type, _ = strconv.Atoi(root.FindElementValue("/msg/appmsg/refermsg/type"))
		msg.ReferInfo.Svrid, _ = strconv.ParseInt(root.FindElementValue("/msg/appmsg/refermsg/svrid"), 10, 64)
		msg.ReferInfo.Displayname = root.FindElementValue("/msg/appmsg/refermsg/displayname")
		msg.ReferInfo.Content = root.FindElementValue("/msg/appmsg/refermsg/content")

		if msg.ReferInfo.Type == Wechat_Message_Type_Misc {
			contentXML := etree.NewDocument()
			if err := contentXML.ReadFromString(msg.ReferInfo.Content); err != nil {
				log.Println("ReadFromString failed:", err)
				return
			}

			root := NewxmlDocument(contentXML)
			msg.ReferInfo.Content = root.FindElementValue("/msg/appmsg/title")
			msg.ReferInfo.SubType, _ = strconv.Atoi(root.FindElementValue("/msg/appmsg/type"))
		}
	}
}

func (P *WechatDataProvider) wechatMessageGetUserInfo(msg *WeChatMessage) {
	who := msg.Talker
	if msg.IsSender == 1 {
		who = P.SelfInfo.UserName
	}

	pinfo, err := P.WechatGetUserInfoByNameOnCache(who)
	if err != nil {
		log.Println("WechatGetUserInfoByNameOnCache:", err)
		return
	}

	msg.UserInfo = *pinfo
}

func (P *WechatDataProvider) wechatFindDBIndex(userName string, time int64, direction Message_Search_Direction) int {
	if direction == Message_Search_Forward {
		index := 0
		for {
			if index >= len(P.msgDBs) {
				return -1
			}
			msgDB := P.msgDBs[index]

			if msgDB.startTime > time {
				index += 1
				continue
			}

			rowId := 0
			querySql := fmt.Sprintf("select rowid from Name2ID where UsrName='%s';", userName)
			err := msgDB.db.QueryRow(querySql).Scan(&rowId)
			if err != nil {
				log.Printf("Scan: %v\n", err)
				index += 1
				continue
			}

			querySql = fmt.Sprintf(" select rowid from MSG where StrTalker='%s' AND CreateTime<=%d limit 1;", userName, time)
			log.Printf("in %s, %s\n", msgDB.path, querySql)
			err = msgDB.db.QueryRow(querySql).Scan(&rowId)
			if err != nil {
				log.Printf("Scan: %v\n", err)
				index += 1
				continue
			}

			log.Printf("Select in %d %s\n", index, msgDB.path)
			return index
		}
	} else {
		index := len(P.msgDBs) - 1
		for {
			if index < 0 {
				return -1
			}
			msgDB := P.msgDBs[index]

			if msgDB.endTime < time {
				index -= 1
				continue
			}

			rowId := 0
			querySql := fmt.Sprintf("select rowid from Name2ID where UsrName='%s';", userName)
			err := msgDB.db.QueryRow(querySql).Scan(&rowId)
			if err != nil {
				log.Printf("Scan: %v\n", err)
				index -= 1
				continue
			}

			querySql = fmt.Sprintf(" select rowid from MSG where StrTalker='%s' AND CreateTime>%d limit 1;", userName, time)
			log.Printf("in %s, %s\n", msgDB.path, querySql)
			err = msgDB.db.QueryRow(querySql).Scan(&rowId)
			if err != nil {
				log.Printf("Scan: %v\n", err)
				index -= 1
				continue
			}

			log.Printf("Select in %d %s\n", index, msgDB.path)
			return index
		}
	}
}

func (P *WechatDataProvider) wechatGetLastMessageCreateTime(userName string, index int) int64 {
	if index >= len(P.msgDBs) {
		return -1
	}
	sqlFormat := "SELECT CreateTime FROM MSG WHERE StrTalker='%s' order by CreateTime asc limit 1;"
	querySql := fmt.Sprintf(sqlFormat, userName)
	var lastTime int64
	err := P.msgDBs[index].db.QueryRow(querySql).Scan(&lastTime)
	if err != nil {
		log.Println("select DB lastTime failed:", index, ":", err)
		return -1
	}

	return lastTime
}

func weChatMessageContains(msg *WeChatMessage, chars string) bool {

	switch msg.Type {
	case Wechat_Message_Type_Text:
		return strings.Contains(msg.Content, chars)
	case Wechat_Message_Type_Misc:
		switch msg.SubType {
		case Wechat_Misc_Message_CardLink:
			return strings.Contains(msg.LinkInfo.Title, chars) || strings.Contains(msg.LinkInfo.Description, chars)
		case Wechat_Misc_Message_Refer:
			return strings.Contains(msg.Content, chars)
		case Wechat_Misc_Message_File:
			return strings.Contains(msg.FileInfo.FileName, chars)
		default:
			return false
		}
	default:
		return false
	}
}

func weChatMessageTypeFilter(msg *WeChatMessage, msgType string) bool {
	switch msgType {
	case "":
		return true
	case "文件":
		return msg.Type == Wechat_Message_Type_Misc && msg.SubType == Wechat_Misc_Message_File
	case "图片与视频":
		return msg.Type == Wechat_Message_Type_Picture || msg.Type == Wechat_Message_Type_Video
	case "链接":
		return msg.Type == Wechat_Misc_Message_CardLink || msg.SubType == Wechat_Misc_Message_CardLink
	default:
		if strings.HasPrefix(msgType, "群成员") {
			userName := msgType[len("群成员"):]
			return msg.UserInfo.UserName == userName
		}

		return false
	}
}

func wechatOpenMsgDB(path string) (*wechatMsgDB, error) {
	msgDB := wechatMsgDB{}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		log.Printf("open db %s error: %v", path, err)
		return nil, err
	}
	msgDB.db = db
	msgDB.path = path
	querySql := "select CreateTime from MSG order by CreateTime asc limit 1;"
	err = msgDB.db.QueryRow(querySql).Scan(&msgDB.startTime)
	if err != nil {
		log.Println("select DB startTime failed:", path, ":", err)
		return nil, err
	}

	querySql = "select CreateTime from MSG order by CreateTime desc limit 1;"
	err = msgDB.db.QueryRow(querySql).Scan(&msgDB.endTime)
	if err != nil {
		log.Println("select DB endTime failed:", path, ":", err)
		return nil, err
	}

	return &msgDB, nil
}

func (P *WechatDataProvider) WechatGetUserInfoByNameOnCache(name string) (*WeChatUserInfo, error) {

	// log.Printf("who: %s", who)

	P.userInfoMtx.Lock()
	defer P.userInfoMtx.Unlock()

	info, ok := P.userInfoMap[name]
	if ok {
		return &info, nil
	}

	pinfo, err := P.WechatGetUserInfoByName(name)
	if err != nil {
		log.Printf("WechatGetUserInfoByName %s failed: %v\n", name, err)
		return nil, err
	}

	P.userInfoMap[name] = *pinfo

	return pinfo, nil
}
