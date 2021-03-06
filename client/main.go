package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/howeyc/gopass"
	sh "github.com/murphysean/secrethitler"
	tb "github.com/nsf/termbox-go"
)

var (
	ghost   string
	ggameID string
	gtoken  string
	gemail  string
	gplayer sh.Player
	glog    string
)

func init() {
	flag.StringVar(&ghost, "host", "http://localhost:8080", "The host")
	flag.StringVar(&ggameID, "gameid", "nil", "The gameID (required)")
	flag.StringVar(&gemail, "email", "", "Players email address")
	flag.StringVar(&glog, "log", "", "Log (e)vents (s)tate (v)ariables (n)etwork")
}

func main() {
	flag.Parse()
	ctx := context.Background()
	ctx = context.WithValue(ctx, "host", ghost)

	//TODO If the email is not empty, attempt to sign the user in, or create a new player
	if gemail != "" {
		fmt.Printf("Password: ")

		// Silent. For printing *'s use gopass.GetPasswdMasked()
		pass, err := gopass.GetPasswd()
		if err != nil {
			// Handle gopass.ErrInterrupted or getch() read error
			log.Fatalf("error getting password: %v\n", err)
		}

		// Do something with pass
		gtoken, player, err := signIn(ctx, gemail, string(pass))
		if err != nil {
			//TODO If the player doesn't exist, create it
			log.Fatalf("error signing in: %v\n", err)
		}

	}

	//TODO If the gameid is empty, pull down a list of active gameids

	f, err := os.OpenFile("shcli-log-"+ggameID+"-"+gplayer.ID, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v\n", err)
	}
	defer f.Close()

	log.SetOutput(f)
	ctx = context.WithValue(ctx, "gameID", ggameID)
	ctx = context.WithValue(ctx, "playerID", gplayer.ID)
	ctx = context.WithValue(ctx, "token", gtoken)

	err = tb.Init()
	if err != nil {
		log.Panicln(err)
	}
	defer tb.Close()
	tb.SetOutputMode(tb.OutputNormal)
	tb.Sync()

	currIdx := 0
	states := []sh.Game{}

	var myEvent sh.Event
	var myAsserts []sh.AssertEvent
	var myErr string
	var myMessages []string

	ec, sc, err := openSSE(ctx)
	if err != nil {
		log.Println("openss:", err)
		return
	}
	go func() {
		for e := range ec {
			switch e.GetType() {
			case sh.TypeRequestAcknowledge:
				myEvent = e
				if strings.Contains(glog, "v") {
					log.Println("setevent:", e)
				}
			case sh.TypeRequestNominate:
				re := e.(sh.RequestEvent)
				if re.PlayerID == ctx.Value("playerID").(string) {
					myEvent = e
					if strings.Contains(glog, "v") {
						log.Println("setevent:", e)
					}
				}
			case sh.TypeRequestVote:
				myEvent = e
				if strings.Contains(glog, "v") {
					log.Println("setevent:", e)
				}
			case sh.TypeRequestLegislate:
				re := e.(sh.RequestEvent)
				if re.PlayerID == ctx.Value("playerID").(string) {
					myEvent = e
					if strings.Contains(glog, "v") {
						log.Println("setevent:", e)
					}
					if !re.Veto {
						ma := sh.AssertEvent{
							PlayerID:     ctx.Value("playerID").(string),
							BaseEvent:    sh.BaseEvent{Type: sh.TypeAssertPolicies},
							RoundID:      re.RoundID,
							PolicySource: sh.RoundStateLegislating,
							Policies:     re.Policies,
							Token:        re.Token,
						}
						myAsserts = append(myAsserts, ma)
					}
				}
			case sh.TypeRequestExecutiveAction:
				re := e.(sh.RequestEvent)
				if re.PlayerID == ctx.Value("playerID").(string) {
					myEvent = e
					if strings.Contains(glog, "v") {
						log.Println("setevent:", e)
					}
				}
			case sh.TypeGameInformation:
				ie := e.(sh.InformationEvent)
				if ie.PlayerID == ctx.Value("playerID").(string) {
					if ie.Party != "" {
						ma := sh.AssertEvent{
							PlayerID:      ctx.Value("playerID").(string),
							BaseEvent:     sh.BaseEvent{Type: sh.TypeAssertParty},
							RoundID:       ie.RoundID,
							OtherPlayerID: ie.OtherPlayerID,
							Party:         ie.Party,
							Token:         ie.Token,
						}
						myAsserts = append(myAsserts, ma)
					}
					if len(ie.Policies) > 0 {
						ma := sh.AssertEvent{
							PlayerID:     ctx.Value("playerID").(string),
							BaseEvent:    sh.BaseEvent{Type: sh.TypeAssertPolicies},
							RoundID:      ie.RoundID,
							PolicySource: sh.ExecutiveActionPeek,
							Policies:     ie.Policies,
							Token:        ie.Token,
						}
						myAsserts = append(myAsserts, ma)
					}
				}
			case sh.TypeAssertParty:
				ae := e.(sh.AssertEvent)
				if ae.PlayerID == ctx.Value("playerID").(string) {
					if len(myAsserts) > 0 && ae.RoundID == myAsserts[0].RoundID {
						myAsserts = myAsserts[1:]
					}
				}
				myMessages = append(myMessages, "Player "+ae.PlayerID+" claims "+ae.OtherPlayerID+" party is "+ae.Party)
			case sh.TypeAssertPolicies:
				ae := e.(sh.AssertEvent)
				if ae.PlayerID == ctx.Value("playerID").(string) {
					if len(myAsserts) > 0 && ae.RoundID == myAsserts[0].RoundID {
						myAsserts = myAsserts[1:]
					}
				}
				if ae.PolicySource == sh.RoundStateLegislating {
					myMessages = append(myMessages, "Player "+ae.PlayerID+" claims they were dealt "+strings.Join(ae.Policies, ", "))
				} else {
					myMessages = append(myMessages, "Player "+ae.PlayerID+" claims they observed "+strings.Join(ae.Policies, ", "))
				}
			case sh.TypeGameVoteResults:
				if myEvent != nil {
					if me, ok := myEvent.(sh.RequestEvent); ok {
						if me.Type == sh.TypeRequestVote {
							myEvent = nil
							if strings.Contains(glog, "v") {
								log.Println("unsetevent:", e)
							}
						}
					}
				}
				vre := e.(sh.VoteResultEvent)
				downvoters := []string{}
				for _, v := range vre.Votes {
					if !v.Vote {
						downvoters = append(downvoters, v.PlayerID)
					}
				}
				if len(downvoters) == 0 {
					downvoters = []string{"nobody"}
				}
				result := "Failed"
				if vre.Succeeded {
					result = "Succeeded"
				}
				myMessages = append(myMessages, "Vote "+result+" with "+strings.Join(downvoters, ", ")+" downvoting")
			case sh.TypePlayerNominate:
				if myEvent != nil {
					if me, ok := myEvent.(sh.RequestEvent); ok {
						if me.Type == sh.TypeRequestNominate {
							myEvent = nil
							if strings.Contains(glog, "v") {
								log.Println("unsetevent:", e)
							}
						}
					}
				}
			case sh.TypePlayerLegislate:
				if myEvent != nil {
					if me, ok := myEvent.(sh.RequestEvent); ok {
						if me.Type == sh.TypeRequestLegislate {
							myEvent = nil
							if strings.Contains(glog, "v") {
								log.Println("unsetevent:", e)
							}
						}
					}
				}
			case sh.TypePlayerSpecialElection:
				fallthrough
			case sh.TypePlayerInvestigate:
				fallthrough
			case sh.TypePlayerExecute:
				if myEvent != nil {
					if me, ok := myEvent.(sh.RequestEvent); ok {
						if me.Type == sh.TypeRequestExecutiveAction {
							myEvent = nil
							if strings.Contains(glog, "v") {
								log.Println("unsetevent:", e)
							}
						}
					}
				}
			}
			tb.Interrupt()
		}
	}()
	go func() {
		for g := range sc {
			states = append(states, g)
			currIdx = len(states) - 1
			tb.Interrupt()
		}
	}()

	for {
		switch ev := tb.PollEvent(); ev.Type {
		case tb.EventKey:
			switch ev.Key {
			case tb.KeyEsc:
				return
			case tb.KeyArrowRight:
				//Go forward an event
				if currIdx < len(states)-1 {
					currIdx++
				}
			case tb.KeyArrowLeft:
				//Go back an event
				if currIdx > 0 {
					currIdx--
				}
			case tb.KeyArrowUp:
				//Go to oldest event
				currIdx = 0
			case tb.KeyArrowDown:
				//Go to newest event
				currIdx = len(states) - 1
			case tb.KeyCtrlJ:
				//Submit join event
				sendEvent(ctx, sh.PlayerEvent{
					BaseEvent: sh.BaseEvent{Type: sh.TypePlayerJoin},
					Player: sh.Player{
						ID: ctx.Value("playerID").(string),
					}})
			case tb.KeyCtrlR:
				//Submit ready event
				sendEvent(ctx, sh.PlayerEvent{
					BaseEvent: sh.BaseEvent{Type: sh.TypePlayerReady},
					Player: sh.Player{
						ID:    ctx.Value("playerID").(string),
						Ready: true,
					}})
			case tb.KeyCtrlA:
				//Submit ack event
				if len(states) > 0 && states[len(states)-1].State == sh.GameStateInit {
					if p, err := states[len(states)-1].GetPlayerByID(ctx.Value("playerID").(string)); err == nil {
						sendEvent(ctx, sh.PlayerEvent{
							BaseEvent: sh.BaseEvent{Type: sh.TypePlayerAcknowledge},
							Player: sh.Player{
								ID:    ctx.Value("playerID").(string),
								Party: p.Party,
								Role:  p.Role,
							}})
					}
				}
			default:
				var se sh.Event
				if len(states) > 0 && myEvent != nil {
					switch ev.Ch {
					case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
						idx := 0
						switch ev.Ch {
						case '1':
							idx = 1
						case '2':
							idx = 2
						case '3':
							idx = 3
						case '4':
							idx = 4
						case '5':
							idx = 5
						case '6':
							idx = 6
						case '7':
							idx = 7
						case '8':
							idx = 8
						case '9':
							idx = 9
						}
						if idx >= len(states[len(states)-1].Players) {
							idx = len(states[len(states)-1].Players) - 1
						}

						if myEvent.GetType() == sh.TypeRequestNominate {
							se = sh.PlayerPlayerEvent{
								BaseEvent:     sh.BaseEvent{Type: sh.TypePlayerNominate},
								PlayerID:      ctx.Value("playerID").(string),
								OtherPlayerID: states[len(states)-1].Players[idx].ID,
							}
						}
						if myEvent.GetType() == sh.TypeRequestExecutiveAction {
							se = sh.PlayerPlayerEvent{
								BaseEvent:     sh.BaseEvent{Type: "player." + myEvent.(sh.RequestEvent).ExecutiveAction},
								PlayerID:      ctx.Value("playerID").(string),
								OtherPlayerID: states[len(states)-1].Players[idx].ID,
							}
						}
					case 'y':
						if myEvent.GetType() == sh.TypeRequestVote {
							se = sh.PlayerVoteEvent{
								BaseEvent: sh.BaseEvent{Type: sh.TypePlayerVote},
								PlayerID:  ctx.Value("playerID").(string),
								Vote:      true,
							}
						}
					case 'n':
						if myEvent.GetType() == sh.TypeRequestVote {
							se = sh.PlayerVoteEvent{
								BaseEvent: sh.BaseEvent{Type: sh.TypePlayerVote},
								PlayerID:  ctx.Value("playerID").(string),
								Vote:      false,
							}
						}
					case 'l':
						se = sh.PlayerLegislateEvent{
							BaseEvent: sh.BaseEvent{Type: sh.TypePlayerLegislate},
							PlayerID:  ctx.Value("playerID").(string),
							Discard:   sh.PolicyLiberal,
						}
					case 'f':
						se = sh.PlayerLegislateEvent{
							BaseEvent: sh.BaseEvent{Type: sh.TypePlayerLegislate},
							PlayerID:  ctx.Value("playerID").(string),
							Discard:   sh.Policyfascist,
						}
					case 'v':
						se = sh.PlayerLegislateEvent{
							BaseEvent: sh.BaseEvent{Type: sh.TypePlayerLegislate},
							PlayerID:  ctx.Value("playerID").(string),
							Veto:      true,
						}
					}
					if se != nil {
						err = sendEvent(ctx, se)
						if err != nil {
							myErr = err.Error()
						} else {
							myEvent = nil
							if strings.Contains(glog, "v") {
								log.Println("unsetevent:", se)
							}
							myErr = ""
						}
					}
				} else if len(states) > 0 && len(myAsserts) > 0 {
					switch ev.Ch {
					case '0':
						if myAsserts[0].Type == sh.TypeAssertPolicies {
							for i, _ := range myAsserts[0].Policies {
								myAsserts[0].Policies[i] = sh.Policyfascist
							}
						}
					case '1':
						if myAsserts[0].Type == sh.TypeAssertPolicies {
							for i, _ := range myAsserts[0].Policies {
								myAsserts[0].Policies[i] = sh.Policyfascist
							}
							myAsserts[0].Policies[0] = sh.PolicyLiberal
						}
					case '2':
						if myAsserts[0].Type == sh.TypeAssertPolicies {
							for i, _ := range myAsserts[0].Policies {
								myAsserts[0].Policies[i] = sh.Policyfascist
							}
							myAsserts[0].Policies[0] = sh.PolicyLiberal
							myAsserts[0].Policies[1] = sh.PolicyLiberal
						}
					case '3':
						if myAsserts[0].Type == sh.TypeAssertPolicies {
							for i, _ := range myAsserts[0].Policies {
								myAsserts[0].Policies[i] = sh.PolicyLiberal
							}
						}
					case 'l':
						if myAsserts[0].Type == sh.TypeAssertParty {
							myAsserts[0].Party = sh.PartyLiberal
						}
					case 'f':
						if myAsserts[0].Type == sh.TypeAssertParty {
							myAsserts[0].Party = sh.Partyfascist
						}
					}
					err = sendEvent(ctx, myAsserts[0])
					if err != nil {
						//Display the error message
						myErr = err.Error()
					} else {
					}
				}
			}
		case tb.EventInterrupt:
			//Got a new state?
		}
		tb.Clear(tb.ColorDefault, tb.ColorDefault)
		if len(states) > 0 {
			drawPlayers(states[currIdx])
			drawGameBoard(states[currIdx])
			drawEventPrompt(states[currIdx], myEvent, myAsserts)
			drawErrorMessage(myErr)
			drawMessages(myMessages)
		}
		tb.Flush()
	}
}

func getNameForID(id string) string {
	return id
}

func drawErrorMessage(es string) {
	if es == "" {
		return
	}
	drawStringAt("Err: "+es, 0, 11, tb.ColorDefault, tb.ColorDefault)
}

func drawMessages(msgs []string) {
	if len(msgs) > 5 {
		msgs = msgs[len(msgs)-5:]
	}
	for i, msg := range msgs {
		drawStringAt(msg, 0, 12+i, tb.ColorDefault, tb.ColorDefault)
	}
}

func drawEventPrompt(g sh.Game, e sh.Event, aes []sh.AssertEvent) {
	cards := g.Fascist + g.Liberal + len(g.Discard) + len(g.Draw) + len(g.Round.Policies)
	drawStringAt(fmt.Sprintf("Policy Count: %d", cards), 0, 20, tb.ColorDefault, tb.ColorDefault)
	if e == nil {
		if len(aes) > 0 {
			ae := aes[0]
			switch ae.GetType() {
			case sh.TypeAssertPolicies:
				if ae.PolicySource == sh.RoundStateLegislating {
					drawStringAt("Tell others how many liberal policies you were dealt (0-3):", 0, 10, tb.ColorDefault, tb.ColorDefault)
				} else {
					drawStringAt("Tell others how many liberal policies are available next round (0-3):", 0, 10, tb.ColorDefault, tb.ColorDefault)
				}
			case sh.TypeAssertParty:
				drawStringAt("Tell others what party you investigated (l/f):", 0, 10, tb.ColorDefault, tb.ColorDefault)
			}
		}
		return
	}
	switch e.GetType() {
	case sh.TypeRequestAcknowledge:
		drawStringAt("Ctrl-A to acknowledge your party/role:", 0, 10, tb.ColorDefault, tb.ColorDefault)
	case sh.TypeRequestNominate:
		if g.Round.State == sh.RoundStateNominating {
			drawStringAt("Choose another player as chancellor (0-9):", 0, 10, tb.ColorDefault, tb.ColorDefault)
		}
	case sh.TypeRequestVote:
		if g.Round.State == sh.RoundStateVoting {
			drawStringAt("Vote y/n on president/chancellor:", 0, 10, tb.ColorDefault, tb.ColorDefault)
		}
	case sh.TypeRequestLegislate:
		le := e.(sh.RequestEvent)
		if g.Round.State == sh.RoundStateLegislating {
			if g.Fascist >= 5 && g.Round.ChancellorID == le.PlayerID {
				drawStringAt("Choose a policy to discard(l/f) or (v)eto:", 0, 10, tb.ColorDefault, tb.ColorDefault)
			} else if le.Veto {
				drawStringAt("Chancelor has requested (v)eto, press f otherwise:", 0, 10, tb.ColorDefault, tb.ColorDefault)
			} else {
				drawStringAt("Choose a policy to discard(l/f):", 0, 10, tb.ColorDefault, tb.ColorDefault)
			}
		}
	case sh.TypeRequestExecutiveAction:
		if g.Round.State == sh.RoundStateExecutiveAction {
			eae := e.(sh.RequestEvent)
			drawStringAt("Choose another player to "+eae.ExecutiveAction+" (0-9):", 0, 10, tb.ColorDefault, tb.ColorDefault)
		}
	}
}

func drawPlayers(g sh.Game) {
	for i, p := range g.Players {
		var fg, bg tb.Attribute
		fg = tb.ColorDefault
		bg = tb.ColorDefault
		switch g.State {
		case sh.GameStateLobby:
			if p.Ready {
				fg = tb.ColorGreen
			}
		case sh.GameStateInit:
			if p.Ack {
				tb.SetCell(0, i, 'A', tb.ColorGreen, tb.ColorDefault)
			}
			fallthrough
		case sh.GameStateStarted:
			switch p.Party {
			case sh.Rolefascist:
				fg = tb.ColorRed
			case sh.RoleLiberal:
				fg = tb.ColorBlue
			}
			if p.Role == sh.RoleHitler {
				fg = tb.ColorYellow
			}
			if p.ExecutedBy != "" {
				tb.SetCell(0, i, 'X', tb.ColorRed, tb.ColorDefault)
			}
			if p.ID == g.Round.PresidentID {
				tb.SetCell(0, i, 'P', tb.ColorGreen, tb.ColorDefault)
			}
			if p.ID == g.Round.ChancellorID {
				tb.SetCell(0, i, 'C', tb.ColorGreen, tb.ColorDefault)
			}
		}
		drawStringAt(getNameForID(p.ID), 1, i, fg, bg)
	}
}

func drawGameBoard(g sh.Game) {
	if g.Liberal > 0 {
		tb.SetCell(20, 0, '█', tb.ColorBlue, tb.ColorDefault)
	} else {
		tb.SetCell(20, 0, '░', tb.ColorBlue, tb.ColorDefault)
	}
	if g.Liberal > 1 {
		tb.SetCell(21, 0, '█', tb.ColorBlue, tb.ColorDefault)
	} else {
		tb.SetCell(21, 0, '░', tb.ColorBlue, tb.ColorDefault)
	}
	if g.Liberal > 2 {
		tb.SetCell(22, 0, '█', tb.ColorBlue, tb.ColorDefault)
	} else {
		tb.SetCell(22, 0, '░', tb.ColorBlue, tb.ColorDefault)
	}
	if g.Liberal > 3 {
		tb.SetCell(23, 0, '█', tb.ColorBlue, tb.ColorDefault)
	} else {
		tb.SetCell(23, 0, '░', tb.ColorBlue, tb.ColorDefault)
	}
	if g.Liberal > 4 {
		tb.SetCell(24, 0, '█', tb.ColorBlue, tb.ColorDefault)
	} else {
		tb.SetCell(24, 0, '░', tb.ColorBlue, tb.ColorDefault)
	}

	tb.SetCell(21, 1, '.', tb.ColorDefault, tb.ColorDefault)
	tb.SetCell(22, 1, '.', tb.ColorDefault, tb.ColorDefault)
	tb.SetCell(23, 1, '.', tb.ColorDefault, tb.ColorDefault)
	switch g.ElectionTracker {
	case 1:
		tb.SetCell(21, 1, 'x', tb.ColorDefault, tb.ColorDefault)
	case 2:
		tb.SetCell(22, 1, 'x', tb.ColorDefault, tb.ColorDefault)
	case 3:
		tb.SetCell(23, 1, 'x', tb.ColorDefault, tb.ColorDefault)
	}
	if g.WinningParty != "" {
		drawStringAt("Game Over - "+g.WinningParty+" Win!", 20, 3, tb.ColorDefault, tb.ColorDefault)
	}

	if g.Fascist > 0 {
		tb.SetCell(20, 2, '█', tb.ColorRed, tb.ColorDefault)
	} else {
		tb.SetCell(20, 2, '░', tb.ColorRed, tb.ColorDefault)
	}
	if g.Fascist > 1 {
		tb.SetCell(21, 2, '█', tb.ColorRed, tb.ColorDefault)
	} else {
		tb.SetCell(21, 2, '░', tb.ColorRed, tb.ColorDefault)
	}
	if g.Fascist > 2 {
		tb.SetCell(22, 2, '█', tb.ColorRed, tb.ColorDefault)
	} else {
		tb.SetCell(22, 2, '░', tb.ColorRed, tb.ColorDefault)
	}
	if g.Fascist > 3 {
		tb.SetCell(23, 2, '█', tb.ColorRed, tb.ColorDefault)
	} else {
		tb.SetCell(23, 2, '░', tb.ColorRed, tb.ColorDefault)
	}
	if g.Fascist > 4 {
		tb.SetCell(24, 2, '█', tb.ColorRed, tb.ColorDefault)
	} else {
		tb.SetCell(24, 2, '░', tb.ColorRed, tb.ColorDefault)
	}
	if g.Fascist > 5 {
		tb.SetCell(25, 2, '█', tb.ColorRed, tb.ColorDefault)
	} else {
		tb.SetCell(25, 2, '░', tb.ColorRed, tb.ColorDefault)
	}

	for i, p := range g.Draw {
		char := '?'
		fg := tb.ColorDefault
		switch p {
		case sh.Policyfascist:
			char = 'F'
			fg = tb.ColorRed
		case sh.PolicyLiberal:
			char = 'L'
			fg = tb.ColorBlue
		}
		switch {
		case i == len(g.Draw)-1:
			tb.SetCell(27, 0, char, fg, tb.ColorDefault)
		case i == len(g.Draw)-2:
			tb.SetCell(27, 1, char, fg, tb.ColorDefault)
		case i == len(g.Draw)-3:
			tb.SetCell(27, 2, char, fg, tb.ColorDefault)
		}
	}
	for i, p := range g.Discard {
		char := '?'
		fg := tb.ColorDefault
		switch p {
		case sh.Policyfascist:
			char = 'F'
			fg = tb.ColorRed
		case sh.PolicyLiberal:
			char = 'L'
			fg = tb.ColorBlue
		}
		switch {
		case i == len(g.Discard)-1:
			tb.SetCell(18, 0, char, fg, tb.ColorDefault)
		case i == len(g.Discard)-2:
			tb.SetCell(18, 1, char, fg, tb.ColorDefault)
		case i == len(g.Discard)-3:
			tb.SetCell(18, 2, char, fg, tb.ColorDefault)
		}
	}

	for i, p := range g.Round.Policies {
		switch p {
		case sh.Policyfascist:
			tb.SetCell(20+i, 4, 'F', tb.ColorRed, tb.ColorDefault)
		case sh.PolicyLiberal:
			tb.SetCell(20+i, 4, 'L', tb.ColorBlue, tb.ColorDefault)
		default:
			tb.SetCell(20+i, 4, '?', tb.ColorDefault, tb.ColorDefault)
		}
	}
}

func drawStringAt(s string, x, y int, fg, bg tb.Attribute) {
	for _, r := range s {
		tb.SetCell(x, y, r, fg, bg)
		x = x + 1
	}
}

func signIn(ctx context.Context, email, password string) (string, *sh.Player, error) {
	creds := struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{
		Username: email,
		Password: password,
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	err := enc.Encode(&creds)
	if err != nil {
		return "", nil, err
	}
	resp, err := http.Post(ctx.Value("host").(string)+"/api/login", "application/json", &b)
	ret := struct {
		Token  string     `json:"token"`
		Player *sh.Player `json:"player"`
	}{}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, errors.New("bad status:" + resp.Status)
	}
	d := json.NewDecoder(resp.Body)
	err = d.Decode(&ret)
	if err != nil {
		return "", nil, err
	}

	return ret.Token, ret.Player, nil

}

func createPlayer(ctx context.Context, player sh.Player) (*sh.Player, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	err := enc.Encode(&player)
	if err != nil {
		return nil, err
	}
	resp, err := http.Post(ctx.Value("host").(string)+"/api/player", "application/json", &b)
	var ret *sh.Player
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("bad status:" + resp.Status)
	}
	d := json.NewDecoder(resp.Body)
	err = d.Decode(&ret)
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func getPlayer(ctx context.Context, playerID string) (sh.Player, error) {
	resp, err := http.Get(ctx.Value("host").(string) + "/api/players/" + playerID)
}

func openSSE(ctx context.Context) (<-chan sh.Event, <-chan sh.Game, error) {
	resp, err := http.Get(ctx.Value("host").(string) + "/api/games/" + ctx.Value("gameID").(string) + "/events" + "?playerID=" + ctx.Value("playerID").(string))
	if err != nil {
		return nil, nil, err
	}

	ec := make(chan sh.Event)
	gc := make(chan sh.Game)

	br := bufio.NewReader(resp.Body)
	go func() {
		defer resp.Body.Close()
		var event []byte
		for {
			b, err := br.ReadBytes('\n')
			if err != nil {
				//log.Println("opensse:readbytes:", err)
				return
			}
			i := bytes.Index(b, []byte(":"))
			if i > 0 {
				switch {
				case bytes.HasPrefix(b, []byte("event: ")):
					event = bytes.TrimSpace(b[6:])
				case bytes.HasPrefix(b, []byte("data: ")):
					switch {
					case bytes.Equal(event, []byte("state")):
						g := sh.Game{}
						err := json.Unmarshal(b[5:], &g)
						if err != nil {
							continue
						}
						if strings.Contains(glog, "s") {
							log.Println("opensse:sending:game:", g)
						}
						gc <- g
					default:
						e, err := sh.UnmarshalEvent(b[5:])
						if err != nil {
							continue
						}
						if strings.Contains(glog, "e") {
							log.Println("opensse:sending:event:", e)
						}
						ec <- e
					}
				}
			}
		}
	}()

	return ec, gc, nil
}

func sendEvent(ctx context.Context, e sh.Event) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	err := enc.Encode(&e)
	if err != nil {
		return err
	}
	resp, err := http.Post(ctx.Value("host").(string)+"/api/games/"+ctx.Value("gameID").(string)+"/events"+"?playerID="+ctx.Value("playerID").(string), "application/json", &b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := ioutil.ReadAll(resp.Body)
		return errors.New(string(b))
	}

	return nil
}
