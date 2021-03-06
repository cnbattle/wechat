package wechat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const success = `0`

type syncMessageRequest struct {
	SyncKey     map[string]interface{}
	RR          int64 `json:"rr"`
	BaseRequest *BaseRequest
}

type syncMessageResponse struct {
	Response
	SyncKey      map[string]interface{}
	SyncCheckKey map[string]interface{}
	SKey         string
	ContinueFlag int

	// Content
	AddMsgCount            int
	AddMsgList             []map[string]interface{}
	ModContactCount        int
	ModContactList         []map[string]interface{}
	DelContactCount        int
	DelContactList         []map[string]interface{}
	ModChatRoomMemberCount int
	ModChatRoomMemberList  []map[string]interface{}
}

// CountedContent is a Wrappered for data struct from wx server
type CountedContent struct {
	Count   int
	Content []map[string]interface{}
}

// listen did hold a long connection, retrun data by 4 chans.
func (wechat *WeChat) beginSync() error {

	logger.Info(`looking up sync server, after discover sync server you can begin receiving message.`)

	didGetSyncHost := wechat.choseAvalibleSyncHost()

	if !didGetSyncHost {
		return fmt.Errorf(`can't pick an avalible sync host, please re-login`)
	}

	logger.Infof(`discovered sync host [%s], begin sync ... ...`, wechat.syncHost)

	for {
		logger.Info(`sync ....`)

		code, selector, err := wechat.syncCheck()

		if err != nil {
			return err
		}

		if code != success {
			return fmt.Errorf(`syncing failed, please relogin code=%s`, code)
		}

		if selector == `0` {
			logger.Debug(`server is silent`)
		} else {
			continueFlag := -1
			for continueFlag != 0 {
				resp, err := wechat.sync()
				if err != nil {
					logger.Error(err)
					return errors.New(`sync message failed`)
				}
				continueFlag = resp.ContinueFlag

				if resp.ModContactCount > 0 {
					wechat.contactDidChange(resp.ModContactList, Modify)
				}
				if resp.DelContactCount > 0 {
					wechat.contactDidChange(resp.DelContactList, Delete)
				}
				if resp.ModChatRoomMemberCount > 0 {
					wechat.groupMemberDidChange(resp.ModChatRoomMemberList)
				}
				logger.Debugf(`server sync summary:
					AddNewMessage(s)    : %d
					ModContact(s)       : %d
					DelContact(s)       : %d
					ModChatRoomMember(s): %d `,
					resp.AddMsgCount, resp.ModContactCount,
					resp.DelContactCount, resp.ModChatRoomMemberCount)
				go wechat.handleServerEvent(resp)
			}
		}
	}
}

func (wechat *WeChat) syncCheck() (string, string, error) {

	info := url.Values{}
	info.Add("r", fmt.Sprintf("%v", time.Now().Unix()*1000))
	info.Add("sid", wechat.BaseRequest.Wxsid)
	info.Add("uin", fmt.Sprint(wechat.BaseRequest.Wxuin))
	info.Add("deviceid", wechat.BaseRequest.DeviceID)
	info.Add("synckey", wechat.formattedSyncCheckKey())
	info.Add("_", fmt.Sprintf("%v", time.Now().Unix()*1000))

	url, _ := url.Parse(fmt.Sprintf("https://%s/cgi-bin/mmwebwx-bin/synccheck", wechat.syncHost))
	url.RawQuery = info.Encode()

	resp, err := wechat.Client.Get(url.String())

	if err != nil {
		return ``, ``, err
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return ``, ``, err
	}

	ds := string(data)

	logger.Debug(ds)

	// TOOD need handle this error
	code, _ := search(ds, `window.synccheck={retcode:"`, `"`)

	selector, err := search(ds, `window.synccheck={retcode:"0",selector:"`, `"}`)

	//
	if len(resp.Cookies()) > 0 {
		wechat.refreshCookieCache(resp.Cookies())
	}
	wechat.refreshBaseInfo()

	return code, selector, err
}

func (wechat *WeChat) choseAvalibleSyncHost() bool {
	hosts := [...]string{
		`webpush.wx.qq.com`,
		`wx2.qq.com`,
		`webpush.wx2.qq.com`,
		`wx8.qq.com`,
		`webpush.wx8.qq.com`,
		`qq.com`,
		`webpush.wx.qq.com`,
		`web2.wechat.com`,
		`webpush.web2.wechat.com`,
		`wechat.com`,
		`webpush.web.wechat.com`,
		`webpush.weixin.qq.com`,
		`webpush.wechat.com`,
		`webpush1.wechat.com`,
		`webpush2.wechat.com`,
		`webpush2.wx.qq.com`}

	for _, host := range hosts {
		logger.Debugf("attempt connect: %s ... ... ", host)
		wechat.syncHost = host
		code, _, _ := wechat.syncCheck()
		if code == `0` {
			return true
		}
		logger.Errorf("%s connect failed", host)
	}

	return false
}

func (wechat *WeChat) formattedSyncCheckKey() string {

	keys, _ := wechat.syncKey["List"].([]interface{})
	//
	// if keys == nil || len(keys) == 0 {
	// 	keys, _ = wechat.SyncKey["List"].([]interface{})
	// }

	synckeys := make([]string, 0)

	for _, key := range keys {
		kv, ok := key.(map[string]interface{})
		if ok {
			f, _ := kv["Val"].(float64)
			synckeys = append(synckeys, fmt.Sprintf("%v_%v", kv["Key"], int64(f)))
		}
	}

	return strings.Join(synckeys, "|")
}

func (wechat *WeChat) sync() (*syncMessageResponse, error) {

	// 由于go会将int64转换为float64， 所以做了这个恶心的动作
	syncKeyf := make(map[string]interface{}, 0)
	keys := strings.Split(wechat.formattedSyncCheckKey(), "|")

	syncKeyf["Count"] = len(keys)

	list := make([]map[string]int64, 0)

	for _, key := range keys {
		kv := strings.Split(key, "_")
		k, _ := strconv.ParseInt(kv[0], 10, 64)
		v, _ := strconv.ParseInt(kv[1], 10, 64)
		kvmap := map[string]int64{"Key": k, "Val": v}
		list = append(list, kvmap)
	}
	syncKeyf["List"] = list

	data, err := json.Marshal(syncMessageRequest{
		BaseRequest: wechat.BaseRequest,
		SyncKey:     syncKeyf,
		RR:          ^time.Now().Unix(),
	})

	if err != nil {
		return nil, err
	}

	resp := new(syncMessageResponse)
	apiURL := fmt.Sprintf(`%s/webwxsync?sid=%s&lang=en_US&=%s`, wechat.BaseURL, wechat.BaseRequest.Wxsid, wechat.SkeyKV())

	err = wechat.Execute(apiURL, bytes.NewReader(data), resp)

	if err != nil {
		return nil, err
	}

	if resp.SyncCheckKey != nil {
		wechat.syncKey = resp.SyncCheckKey
	} else {
		wechat.syncKey = resp.SyncKey
	}

	return resp, nil
}
