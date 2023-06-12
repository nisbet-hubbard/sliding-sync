package syncv3

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"github.com/tidwall/sjson"
)

// Inject 20 rooms with A,B,C as the most recent events. Then do a v3 request [0,3] with a timeline limit of 3
// and make sure we get scrolback for the 4 rooms we care about. Then, restart the server (so it repopulates caches)
// and attempt the same request again, making sure we get the same results. Then add in some "live" v2 events
// and make sure the initial scrollback includes these new live events.
func TestTimelines(t *testing.T) {
	// setup code
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()

	// make 20 rooms, last room is most recent, and send A,B,C into each room
	allRooms := make([]roomEvents, 20)
	for i := 0; i < len(allRooms); i++ {
		ts := time.Now().Add(time.Duration(i) * time.Minute)
		roomName := fmt.Sprintf("My Room %d", i)
		allRooms[i] = roomEvents{
			roomID: fmt.Sprintf("!TestTimelines_%d:localhost", i),
			name:   roomName,
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": roomName}, testutils.WithTimestamp(ts.Add(3*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "A"}, testutils.WithTimestamp(ts.Add(4*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "B"}, testutils.WithTimestamp(ts.Add(5*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "C"}, testutils.WithTimestamp(ts.Add(6*time.Second))),
			}...),
		}
	}
	latestTimestamp := time.Now().Add(10 * time.Hour)
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})

	// most recent 4 rooms
	var wantRooms []roomEvents
	i := 0
	for len(wantRooms) < 4 {
		wantRooms = append(wantRooms, allRooms[len(allRooms)-i-1])
		i++
	}
	numTimelineEventsPerRoom := 3

	t.Run("timelines load initially", testTimelineLoadInitialEvents(v3, aliceToken, len(allRooms), wantRooms, numTimelineEventsPerRoom))
	// restart the server
	v3.restart(t, v2, pqString)
	t.Run("timelines load initially after restarts", testTimelineLoadInitialEvents(v3, aliceToken, len(allRooms), wantRooms, numTimelineEventsPerRoom))
	// inject some live events
	liveEvents := []roomEvents{
		{
			roomID: allRooms[0].roomID,
			events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "ping"}, testutils.WithTimestamp(latestTimestamp.Add(1*time.Minute))),
			},
		},
		{
			roomID: allRooms[1].roomID,
			events: []json.RawMessage{
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "ping2"}, testutils.WithTimestamp(latestTimestamp.Add(2*time.Minute))),
			},
		},
	}
	v2.waitUntilEmpty(t, alice)
	// add these live events to the global view of the timeline
	allRooms[0].events = append(allRooms[0].events, liveEvents[0].events...)
	allRooms[1].events = append(allRooms[1].events, liveEvents[1].events...)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(liveEvents...),
		},
	})
	v2.waitUntilEmpty(t, alice)

	// now we want the new live rooms and then the most recent 2 rooms from before
	wantRooms = append([]roomEvents{
		allRooms[1], allRooms[0],
	}, wantRooms[0:2]...)

	t.Run("live events are added to the timeline initially", testTimelineLoadInitialEvents(v3, aliceToken, len(allRooms), wantRooms, numTimelineEventsPerRoom))
}

// Create 20 rooms and send A,B,C into each. Then bump various rooms "live streamed" from v2 and ensure
// the correct delta operations are sent e.g DELETE/INSERT/UPDATE.
func TestTimelinesLiveStream(t *testing.T) {
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, "")
	defer v2.close()
	defer v3.close()
	// make 20 rooms, last room is most recent, and send A,B,C into each room
	allRooms := make([]roomEvents, 20)
	latestTimestamp := time.Now()
	for i := 0; i < len(allRooms); i++ {
		ts := time.Now().Add(time.Duration(i) * time.Minute)
		roomName := fmt.Sprintf("My Room %d", i)
		allRooms[i] = roomEvents{
			roomID: fmt.Sprintf("!TestTimelinesLiveStream_%d:localhost", i),
			name:   roomName,
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": roomName}, testutils.WithTimestamp(ts.Add(3*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "A"}, testutils.WithTimestamp(ts.Add(4*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "B"}, testutils.WithTimestamp(ts.Add(5*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "C"}, testutils.WithTimestamp(ts.Add(6*time.Second))),
			}...),
		}
		if ts.After(latestTimestamp) {
			latestTimestamp = ts.Add(10 * time.Second)
		}
	}
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})
	numTimelineEventsPerRoom := 3

	// send a live event in allRooms[i] (always 1s newer)
	bumpRoom := func(i int) {
		latestTimestamp = latestTimestamp.Add(1 * time.Second)
		ev := testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": fmt.Sprintf("bump %d", i)}, testutils.WithTimestamp(latestTimestamp))
		allRooms[i].events = append(allRooms[i].events, ev)
		v2.queueResponse(alice, sync2.SyncResponse{
			Rooms: sync2.SyncRoomsResponse{
				Join: v2JoinTimeline(roomEvents{
					roomID: allRooms[i].roomID,
					events: []json.RawMessage{ev},
				}),
			},
		})
		v2.waitUntilEmpty(t, alice)
	}

	// most recent 4 rooms
	var wantRooms []roomEvents
	i := 0
	for len(wantRooms) < 4 {
		wantRooms = append(wantRooms, allRooms[len(allRooms)-i-1])
		i++
	}

	// first request => rooms 19,18,17,16
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: int64(numTimelineEventsPerRoom),
			},
		}},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(len(allRooms)), m.MatchV3Ops(
		m.MatchV3SyncOpFn(func(op *sync3.ResponseOpRange) error {
			if len(op.RoomIDs) != len(wantRooms) {
				return fmt.Errorf("want %d rooms, got %d", len(wantRooms), len(op.RoomIDs))
			}
			for i := range wantRooms {
				err := wantRooms[i].MatchRoom(op.RoomIDs[i],
					res.Rooms[op.RoomIDs[i]],
					m.MatchRoomName(wantRooms[i].name),
					m.MatchRoomTimelineMostRecent(numTimelineEventsPerRoom, wantRooms[i].events),
				)
				if err != nil {
					return err
				}
			}
			return nil
		}),
	)))

	bumpRoom(7)

	// next request, DELETE 3; INSERT 0 7;
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
			},
			// sticky remember the timeline_limit
		}},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(len(allRooms)), m.MatchV3Ops(
		m.MatchV3DeleteOp(3),
		m.MatchV3InsertOp(0, allRooms[7].roomID),
	)), m.MatchRoomSubscription(
		allRooms[7].roomID, m.MatchRoomName(allRooms[7].name), m.MatchRoomTimelineMostRecent(numTimelineEventsPerRoom, allRooms[7].events),
	))

	bumpRoom(7)

	// next request, UPDATE 0 7;
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
			},
		}},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(len(allRooms))), m.MatchNoV3Ops())

	bumpRoom(18)

	// next request, DELETE 2; INSERT 0 18;
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
			},
		}},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(len(allRooms)), m.MatchV3Ops(
		m.MatchV3DeleteOp(2),
		m.MatchV3InsertOp(0, allRooms[18].roomID),
	)), m.MatchRoomSubscription(allRooms[18].roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{allRooms[18].events[len(allRooms[18].events)-1]})))
}

func TestMultipleWindows(t *testing.T) {
	// setup code
	pqString := testutils.PrepareDBConnectionString()
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()

	// make 20 rooms, first room is most recent, and send A,B,C into each room
	allRooms := make([]roomEvents, 20)
	for i := 0; i < len(allRooms); i++ {
		ts := time.Now().Add(time.Duration(i) * -1 * time.Minute)
		roomName := fmt.Sprintf("My Room %d", i)
		allRooms[i] = roomEvents{
			roomID: fmt.Sprintf("!TestMultipleWindows_%d:localhost", i),
			name:   roomName,
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": roomName}, testutils.WithTimestamp(ts.Add(3*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "A"}, testutils.WithTimestamp(ts.Add(4*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "B"}, testutils.WithTimestamp(ts.Add(5*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "C"}, testutils.WithTimestamp(ts.Add(6*time.Second))),
			}...),
		}
	}
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})
	numTimelineEventsPerRoom := 2

	// request 3 windows
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 2},   // first 3 rooms
				[2]int64{10, 12}, // 3 rooms in the middle
				[2]int64{17, 19}, // last 3 rooms
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: int64(numTimelineEventsPerRoom),
			},
		}},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(len(allRooms)), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 2, []string{allRooms[0].roomID, allRooms[1].roomID, allRooms[2].roomID}),
		m.MatchV3SyncOp(10, 12, []string{allRooms[10].roomID, allRooms[11].roomID, allRooms[12].roomID}),
		m.MatchV3SyncOp(17, 19, []string{allRooms[17].roomID, allRooms[18].roomID, allRooms[19].roomID}),
	)))

	// bump room 18 to position 0
	latestTimestamp := time.Now().Add(time.Hour)
	bumpRoom := func(i int) {
		latestTimestamp = latestTimestamp.Add(1 * time.Second)
		ev := testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": fmt.Sprintf("bump %d", i)}, testutils.WithTimestamp(latestTimestamp))
		allRooms[i].events = append(allRooms[i].events, ev)
		v2.queueResponse(alice, sync2.SyncResponse{
			Rooms: sync2.SyncRoomsResponse{
				Join: v2JoinTimeline(roomEvents{
					roomID: allRooms[i].roomID,
					events: []json.RawMessage{ev},
				}),
			},
		})
		v2.waitUntilEmpty(t, alice)
	}
	bumpRoom(18)

	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 2},   // first 3 rooms
				[2]int64{10, 12}, // 3 rooms in the middle
				[2]int64{17, 19}, // last 3 rooms
			},
		}},
	})
	// Range A             Range B              Range C
	// _____               ________             ________
	// 0 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19
	//18 0 1 2 3 4 5 6 7 8 9  10 11 12 13 14 15 16 17 18
	// DELETE 2            DELETE 12            DELETE 18
	// INSERT 0,18         INSERT 10,9          INSERT 17,16
	m.MatchResponse(t, res, m.MatchList("a",
		m.MatchV3Count(len(allRooms)),
		m.MatchV3Ops(
			m.MatchV3DeleteOp(18),
			m.MatchV3InsertOp(17, allRooms[16].roomID),
			m.MatchV3DeleteOp(2),
			m.MatchV3InsertOp(0, allRooms[18].roomID),
			m.MatchV3DeleteOp(12),
			m.MatchV3InsertOp(10, allRooms[9].roomID),
		),
	))

}

func TestInitialFlag(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!a:localhost"
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				state:  createRoomState(t, alice, time.Now()),
			}),
		},
	})
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 10,
			},
		}},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Ops(
		m.MatchV3SyncOp(0, 0, []string{roomID}),
	)), m.MatchRoomSubscription(roomID, m.MatchRoomInitial(true)))
	// send an update
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(
				roomEvents{
					roomID: roomID,
					events: []json.RawMessage{
						testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{}),
					},
				},
			),
		},
	})
	v2.waitUntilEmpty(t, alice)

	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
		}},
	})
	m.MatchResponse(t, res, m.MatchNoV3Ops(), m.MatchRoomSubscription(roomID, m.MatchRoomInitial(false)))
}

// Regression test for in-the-wild bug:
//
//	ERR missing events in database!
//	ERR V2: failed to accumulate room error="failed to extract nids from inserted events, asked for 9 got 8"
//
// We should be able to gracefully handle duplicate events in the timeline.
func TestDuplicateEventsInTimeline(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!a:localhost"

	dupeEvent := testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{})
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				state:  createRoomState(t, alice, time.Now()),
				events: []json.RawMessage{
					testutils.NewStateEvent(t, "m.room.topic", "", alice, map[string]interface{}{}),
					dupeEvent, dupeEvent,
				},
			}),
		},
	})
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 10,
			},
		}},
	})
	m.MatchResponse(t, res, m.MatchList("a",
		m.MatchV3Ops(m.MatchV3SyncOp(0, 0, []string{roomID})),
	), m.MatchRoomSubscription(roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{dupeEvent})))
}

// Regression test for https://github.com/matrix-org/sliding-sync/commit/39d6e99f967e55b609f8ef8b4271c04ebb053d37
// Request a timeline_limit of 0 for the room list. Sometimes when a new event arrives it causes an
// unrelated room to be sent to the client (e.g tracking rooms [5,10] and room 15 bumps to room 2,
// causing all the rooms to shift so you're now actually tracking [4,9] - the client knows 5-9 but
// room 4 is new, so you notify about that room and not the one which had a new event (room 15).
// Ensure that room 4 is given to the client. In the past, this would panic when timeline limit = 0
// as the timeline was loaded using the timeline limit of the client, and an unchecked array access
// into the timeline
func TestTimelineMiddleWindowZeroTimelineLimit(t *testing.T) {
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, "")
	defer v2.close()
	defer v3.close()
	// make 20 rooms, first room is most recent, and send A,B,C into each room
	allRooms := make([]roomEvents, 20)
	for i := 0; i < len(allRooms); i++ {
		ts := time.Now().Add(-1 * time.Duration(i) * time.Minute)
		roomName := fmt.Sprintf("My Room %d", i)
		allRooms[i] = roomEvents{
			roomID: fmt.Sprintf("!TestTimelineMiddleWindowZeroTimelineLimit_%d:localhost", i),
			name:   roomName,
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": roomName}, testutils.WithTimestamp(ts.Add(3*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "A"}, testutils.WithTimestamp(ts.Add(4*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "B"}, testutils.WithTimestamp(ts.Add(5*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "C"}, testutils.WithTimestamp(ts.Add(6*time.Second))),
			}...),
		}
	}
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})

	// Request rooms 5-10 with a 0 timeline limit
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{5, 10},
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 0,
			},
		}},
	})
	wantRooms := allRooms[5:11]
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(len(allRooms)), m.MatchV3Ops(
		m.MatchV3SyncOpFn(func(op *sync3.ResponseOpRange) error {
			if len(op.RoomIDs) != len(wantRooms) {
				return fmt.Errorf("want %d rooms, got %d", len(wantRooms), len(op.RoomIDs))
			}
			for i := range wantRooms {
				err := wantRooms[i].MatchRoom(op.RoomIDs[i],
					res.Rooms[op.RoomIDs[i]],
					m.MatchRoomName(wantRooms[i].name),
					m.MatchRoomTimeline(nil),
				)
				if err != nil {
					return err
				}
			}
			return nil
		}),
	)))

	// bump room 15 to 2
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: allRooms[15].roomID,
				events: []json.RawMessage{
					testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "bump"}),
				},
			}),
		},
	})
	v2.waitUntilEmpty(t, alice)

	// should see room 4, the server should not panic
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{5, 10},
			},
		}},
	})
	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		"a": {
			m.MatchV3Count(len(allRooms)),
			m.MatchV3Ops(
				m.MatchV3DeleteOp(10),
				m.MatchV3InsertOp(5, allRooms[4].roomID),
			),
		},
	}))
}

// Regression test to ensure that the 'state' block NEVER appears when requesting a high timeline_limit.
// In the past, the proxy treated state/timeline sections as the 'same' in that they were inserted into the
// events table and had stream positions associated with them. This could cause ancient state events to appear
// in the timeline if the timeline_limit was greatert than the number of genuine timeline events received via
// v2 sync.
func TestHistoryDoesntIncludeState(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!a:localhost"

	prevBatch := "P_B"
	room := roomEvents{
		roomID: roomID,
		// these events should NEVER appear in the timeline
		state: createRoomState(t, alice, time.Now()),
		// these events are the timeline and should appear
		events: []json.RawMessage{
			testutils.NewStateEvent(t, "m.room.topic", "", alice, map[string]interface{}{"topic": "boo"}),
			testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "hello"}),
		},
		prevBatch: prevBatch,
	}
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(room),
		},
	})
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 10,
			},
		}},
	})
	m.MatchResponse(t, res, m.MatchList("a",
		m.MatchV3Ops(m.MatchV3SyncOp(0, 0, []string{roomID})),
	), m.MatchRoomSubscription(roomID, m.MatchRoomTimeline(room.events), m.MatchRoomPrevBatch(prevBatch)))
}

// Test that transaction IDs come down the user's stream correctly in the case where 2 clients are
// in the same room.
func TestTimelineTxnID(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!a:localhost"
	latestTimestamp := time.Now()
	// Alice and Bob are in the same room
	room := roomEvents{
		roomID: roomID,
		events: append(
			createRoomState(t, alice, latestTimestamp),
			testutils.NewJoinEvent(t, bob),
		),
	}
	v2.addAccount(t, alice, aliceToken)
	v2.addAccount(t, bob, bobToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(room),
		},
	})
	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(room),
		},
	})

	aliceRes := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 2,
			},
		},
		},
	})
	bobRes := v3.mustDoV3Request(t, bobToken, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 2,
			},
		},
		},
	})

	// Alice has sent a message but it arrives down Bob's poller first, so it has no txn_id
	txnID := "m1234567890"
	newEvent := testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "hi"}, testutils.WithUnsigned(map[string]interface{}{
		"transaction_id": txnID,
	}))
	newEventNoUnsigned, err := sjson.DeleteBytes(newEvent, "unsigned")
	if err != nil {
		t.Fatalf("failed to delete bytes: %s", err)
	}
	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{newEventNoUnsigned},
			}),
		},
	})
	v2.waitUntilEmpty(t, bob)

	// now it arrives down Alice's poller, but the event has already been persisted at this point!
	// We need a txn ID cache to remember it.
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{newEvent},
			}),
		},
	})
	v2.waitUntilEmpty(t, alice)

	// now Alice syncs, she should see the event with the txn ID
	aliceRes = v3.mustDoV3RequestWithPos(t, aliceToken, aliceRes.Pos, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
		},
		},
	})
	m.MatchResponse(t, aliceRes, m.MatchList("a", m.MatchV3Count(1)), m.MatchNoV3Ops(), m.MatchRoomSubscription(
		roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{newEvent}),
	))

	// now Bob syncs, he should see the event without the txn ID
	bobRes = v3.mustDoV3RequestWithPos(t, bobToken, bobRes.Pos, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
		},
		},
	})
	m.MatchResponse(t, bobRes, m.MatchList("a", m.MatchV3Count(1)), m.MatchNoV3Ops(), m.MatchRoomSubscription(
		roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{newEventNoUnsigned}),
	))
}

// Like TestTimelineTxnID, but where the user sync responds to a live update.
func TestTimelineTxnIDDuringLiveUpdate(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!a:localhost"
	latestTimestamp := time.Now()
	t.Log("Alice and Bob are in the same room")
	room := roomEvents{
		roomID: roomID,
		events: append(
			createRoomState(t, alice, latestTimestamp),
			testutils.NewJoinEvent(t, bob),
		),
	}
	v2.addAccount(t, alice, aliceToken)
	v2.addAccount(t, bob, bobToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(room),
		},
		NextBatch: "alice_after_initial_poll",
	})
	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(room),
		},
		NextBatch: "bob_after_initial_poll",
	})

	aliceRes := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 2,
			},
		},
		},
	})
	bobRes := v3.mustDoV3Request(t, bobToken, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 2,
			},
		},
		},
	})

	resChan := make(chan *sync3.Response)
	go func() {
		t.Log("Alice requests an incremental sliding sync.")
		resChan <- v3.mustDoV3RequestWithPos(t, aliceToken, aliceRes.Pos, sync3.Request{
			Lists: map[string]sync3.RequestList{
				"a": {
					Ranges: sync3.SliceRanges{[2]int64{0, 10}},
				},
			},
		})
	}()

	// We want to ensure that the sync handler is waiting for an event to give to alice
	// before we queue messages on the pollers. The request will timeout after 20ms;
	// sleep for half of that.
	/// TODO: is there a better way other than a sleep?
	time.Sleep(10 * time.Millisecond)

	t.Log("Alice has sent a message... but it arrives down Bob's poller first, without a transaction_id")
	txnID := "m1234567890"
	newEvent := testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "hi"}, testutils.WithUnsigned(map[string]interface{}{
		"transaction_id": txnID,
	}))
	newEventNoUnsigned, err := sjson.DeleteBytes(newEvent, "unsigned")
	if err != nil {
		t.Fatalf("failed to delete bytes: %s", err)
	}

	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{newEventNoUnsigned},
			}),
		},
	})
	t.Log("Bob's poller sees the message.")
	v2.waitUntilEmpty(t, bob)

	// now it arrives down Alice's poller, but the event has already been persisted at this point!
	// We need a txn ID cache to remember it.
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{newEvent},
			}),
		},
	})
	t.Log("Alice's poller sees the message.")
	v2.waitUntilEmpty(t, alice)

	select {
	case aliceRes = <-resChan:
		t.Log("Alice's sync response includes the message with the txn ID.")
		m.MatchResponse(t, aliceRes, m.MatchList("a", m.MatchV3Count(1)), m.MatchNoV3Ops(), m.MatchRoomSubscription(
			roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{newEvent}),
		))
	case <-time.After(1 * time.Second):
		t.Fatalf("Alice did not see a sync response in time.")
	}

	t.Log("Bob makes an incremental sliding sync")
	bobRes = v3.mustDoV3RequestWithPos(t, bobToken, bobRes.Pos, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
		},
		},
	})
	t.Log("Bob should see the message without a transaction_id")
	m.MatchResponse(t, bobRes, m.MatchList("a", m.MatchV3Count(1)), m.MatchNoV3Ops(), m.MatchRoomSubscription(
		roomID, m.MatchRoomTimelineMostRecent(1, []json.RawMessage{newEventNoUnsigned}),
	))
}

// Executes a sync v3 request without a ?pos and asserts that the count, rooms and timeline events m.Match the inputs given.
func testTimelineLoadInitialEvents(v3 *testV3Server, token string, count int, wantRooms []roomEvents, numTimelineEventsPerRoom int) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		res := v3.mustDoV3Request(t, token, sync3.Request{
			Lists: map[string]sync3.RequestList{"a": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, int64(len(wantRooms) - 1)}, // first N rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: int64(numTimelineEventsPerRoom),
				},
			}},
		})

		m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(count), m.MatchV3Ops(
			m.MatchV3SyncOpFn(func(op *sync3.ResponseOpRange) error {
				if len(op.RoomIDs) != len(wantRooms) {
					return fmt.Errorf("want %d rooms, got %d", len(wantRooms), len(op.RoomIDs))
				}
				for i := range wantRooms {
					err := wantRooms[i].MatchRoom(op.RoomIDs[i],
						res.Rooms[op.RoomIDs[i]],
						m.MatchRoomName(wantRooms[i].name),
						m.MatchRoomTimelineMostRecent(numTimelineEventsPerRoom, wantRooms[i].events),
					)
					if err != nil {
						return err
					}
				}
				return nil
			}),
		)))
	}
}

// Test that prev batch tokens appear correctly.
// 1: When there is no newer prev_batch, none is present.
// 2: When there is a newer prev_batch, it is present.
func TestPrevBatchInTimeline(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	roomID := "!a:localhost"
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				prevBatch: "create",
				roomID:    roomID,
				state:     createRoomState(t, alice, time.Now()),
				events: []json.RawMessage{
					testutils.NewStateEvent(t, "m.room.topic", "", alice, map[string]interface{}{}),
					testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "hello"}),
				},
			}),
		},
	})
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{"a": {
			Ranges: sync3.SliceRanges{
				[2]int64{0, 10},
			},
			RoomSubscription: sync3.RoomSubscription{
				TimelineLimit: 1,
			},
		}},
	})
	m.MatchResponse(t, res, m.MatchList("a",
		m.MatchV3Ops(
			m.MatchV3SyncOp(0, 0, []string{roomID}),
		),
	), m.MatchRoomSubscription(roomID, m.MatchRoomPrevBatch("")))

	// now make a newer prev_batch and try again
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				prevBatch: "newer",
				roomID:    roomID,
				events: []json.RawMessage{
					testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "hello 2"}),
				},
			}),
		},
	})
	v2.waitUntilEmpty(t, alice)

	testCases := []struct {
		timelineLimit int64
		wantPrevBatch string
	}{
		{
			timelineLimit: 1,
			wantPrevBatch: "newer", // the latest event m.Matches the start of the timeline for the new sync, so prev batches align
		},
		{
			timelineLimit: 2,
			// the end of the timeline for the initial sync, we do not have a prev batch for this event.
			// we cannot return 'create' here else we will miss the topic event before this event
			// hence we return the cloest prev batch which is later than this event and hope clients can
			// deal with dupes.
			wantPrevBatch: "newer",
		},
		{
			timelineLimit: 3,
			wantPrevBatch: "create", // the topic event, the start of the timeline for the initial sync, so prev batches align
		},
	}
	for _, tc := range testCases {
		res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
			Lists: map[string]sync3.RequestList{"a": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 10},
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: tc.timelineLimit,
				},
			}},
		})
		m.MatchResponse(t, res, m.MatchList("a",
			m.MatchV3Ops(
				m.MatchV3SyncOp(0, 0, []string{roomID}),
			),
		), m.MatchRoomSubscription(roomID, m.MatchRoomPrevBatch(tc.wantPrevBatch)))
	}
}

// Test that you can get a window with timeline_limit: 1, then increase the limit to 3 and get the
// room timeline changes only (without any req_state or list ops sent). Likewise, do the same
// but for required_state (initially empty, then set stuff and only get that)
func TestTrickling(t *testing.T) {
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, "")
	defer v2.close()
	defer v3.close()
	// make 10 rooms, first room is most recent, and send A,B,C into each room
	allRooms := make([]roomEvents, 10)
	for i := 0; i < len(allRooms); i++ {
		ts := time.Now().Add(-1 * time.Duration(i) * time.Minute)
		roomName := fmt.Sprintf("My Room %d", i)
		allRooms[i] = roomEvents{
			roomID: fmt.Sprintf("!TestTimelineTrickle_%d:localhost", i),
			name:   roomName,
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(t, "m.room.name", "", alice, map[string]interface{}{"name": roomName}, testutils.WithTimestamp(ts.Add(3*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "A"}, testutils.WithTimestamp(ts.Add(4*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "B"}, testutils.WithTimestamp(ts.Add(5*time.Second))),
				testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "C"}, testutils.WithTimestamp(ts.Add(6*time.Second))),
			}...),
		}
	}
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})

	// always request the top 3 rooms
	testCases := []struct {
		name            string
		initialSub      sync3.RoomSubscription
		nextSub         sync3.RoomSubscription
		wantInitialSubs map[string][]m.RoomMatcher
		wantNextSubs    map[string][]m.RoomMatcher
	}{
		{
			name: "Timeline trickling",
			initialSub: sync3.RoomSubscription{
				TimelineLimit: 1,
				RequiredState: [][2]string{{"m.room.create", ""}},
			},
			wantInitialSubs: map[string][]m.RoomMatcher{
				allRooms[0].roomID: {
					m.MatchRoomTimeline([]json.RawMessage{allRooms[0].events[len(allRooms[0].events)-1]}),
					m.MatchRoomRequiredState([]json.RawMessage{allRooms[0].events[0]}),
				},
				allRooms[1].roomID: {
					m.MatchRoomTimeline([]json.RawMessage{allRooms[1].events[len(allRooms[1].events)-1]}),
					m.MatchRoomRequiredState([]json.RawMessage{allRooms[1].events[0]}),
				},
				allRooms[2].roomID: {
					m.MatchRoomTimeline([]json.RawMessage{allRooms[2].events[len(allRooms[2].events)-1]}),
					m.MatchRoomRequiredState([]json.RawMessage{allRooms[2].events[0]}),
				},
			},
			nextSub: sync3.RoomSubscription{
				TimelineLimit: 3,
				RequiredState: [][2]string{{"m.room.create", ""}},
			},
			wantNextSubs: map[string][]m.RoomMatcher{
				allRooms[0].roomID: {
					m.MatchRoomTimeline(allRooms[0].events[len(allRooms[0].events)-3:]),
					m.MatchRoomRequiredState(nil),
				},
				allRooms[1].roomID: {
					m.MatchRoomTimeline(allRooms[1].events[len(allRooms[1].events)-3:]),
					m.MatchRoomRequiredState(nil),
				},
				allRooms[2].roomID: {
					m.MatchRoomTimeline(allRooms[2].events[len(allRooms[2].events)-3:]),
					m.MatchRoomRequiredState(nil),
				},
			},
		},
		{
			name: "Required State trickling",
			initialSub: sync3.RoomSubscription{
				TimelineLimit: 1,
				RequiredState: [][2]string{{"m.room.create", ""}},
			},
			wantInitialSubs: map[string][]m.RoomMatcher{
				allRooms[0].roomID: {
					m.MatchRoomTimeline([]json.RawMessage{allRooms[0].events[len(allRooms[0].events)-1]}),
					m.MatchRoomRequiredState([]json.RawMessage{allRooms[0].events[0]}),
				},
				allRooms[1].roomID: {
					m.MatchRoomTimeline([]json.RawMessage{allRooms[1].events[len(allRooms[1].events)-1]}),
					m.MatchRoomRequiredState([]json.RawMessage{allRooms[1].events[0]}),
				},
				allRooms[2].roomID: {
					m.MatchRoomTimeline([]json.RawMessage{allRooms[2].events[len(allRooms[2].events)-1]}),
					m.MatchRoomRequiredState([]json.RawMessage{allRooms[2].events[0]}),
				},
			},
			// now add in the room member event
			nextSub: sync3.RoomSubscription{
				TimelineLimit: 1,
				RequiredState: [][2]string{{"m.room.create", ""}, {"m.room.member", alice}},
			},
			wantNextSubs: map[string][]m.RoomMatcher{
				allRooms[0].roomID: {
					m.MatchRoomTimeline(nil),
					m.MatchRoomRequiredState([]json.RawMessage{allRooms[0].events[0], allRooms[0].events[1]}),
				},
				allRooms[1].roomID: {
					m.MatchRoomTimeline(nil),
					m.MatchRoomRequiredState([]json.RawMessage{allRooms[1].events[0], allRooms[1].events[1]}),
				},
				allRooms[2].roomID: {
					m.MatchRoomTimeline(nil),
					m.MatchRoomRequiredState([]json.RawMessage{allRooms[2].events[0], allRooms[2].events[1]}),
				},
			},
		},
		{
			name: "Timeline trickling from 0",
			initialSub: sync3.RoomSubscription{
				TimelineLimit: 0,
			},
			wantInitialSubs: map[string][]m.RoomMatcher{
				allRooms[0].roomID: {
					m.MatchRoomTimeline(nil),
					m.MatchRoomRequiredState(nil),
				},
				allRooms[1].roomID: {
					m.MatchRoomTimeline(nil),
					m.MatchRoomRequiredState(nil),
				},
				allRooms[2].roomID: {
					m.MatchRoomTimeline(nil),
					m.MatchRoomRequiredState(nil),
				},
			},
			nextSub: sync3.RoomSubscription{
				TimelineLimit: 1,
			},
			wantNextSubs: map[string][]m.RoomMatcher{
				allRooms[0].roomID: {
					m.MatchRoomTimeline([]json.RawMessage{allRooms[0].events[len(allRooms[0].events)-1]}),
					m.MatchRoomRequiredState(nil),
				},
				allRooms[1].roomID: {
					m.MatchRoomTimeline([]json.RawMessage{allRooms[1].events[len(allRooms[1].events)-1]}),
					m.MatchRoomRequiredState(nil),
				},
				allRooms[2].roomID: {
					m.MatchRoomTimeline([]json.RawMessage{allRooms[2].events[len(allRooms[2].events)-1]}),
					m.MatchRoomRequiredState(nil),
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Logf(tc.name)
		// request top 3 rooms with initial subscription
		res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
			Lists: map[string]sync3.RequestList{
				"a": {
					Ranges:           [][2]int64{{0, 2}},
					Sort:             []string{sync3.SortByRecency},
					RoomSubscription: tc.initialSub,
				},
			},
		})
		m.MatchResponse(t, res,
			m.MatchList("a", m.MatchV3Ops(m.MatchV3SyncOp(0, 2, []string{allRooms[0].roomID, allRooms[1].roomID, allRooms[2].roomID}))),
			m.MatchRoomSubscriptionsStrict(tc.wantInitialSubs),
		)

		// next request changes the subscription
		res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{
			Lists: map[string]sync3.RequestList{
				"a": {
					Ranges:           [][2]int64{{0, 2}},
					Sort:             []string{sync3.SortByRecency},
					RoomSubscription: tc.nextSub,
				},
			},
		})
		// assert we got what we were expecting
		m.MatchResponse(t, res, m.MatchNoV3Ops(),
			m.MatchRoomSubscriptionsStrict(tc.wantNextSubs),
		)
	}
}

func TestNumLiveBulk(t *testing.T) {
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, "")
	defer v2.close()
	defer v3.close()

	roomID := "!bulk:test"
	v2.addAccount(t, alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				state:  createRoomState(t, alice, time.Now()),
				events: []json.RawMessage{
					testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "A"}, testutils.WithTimestamp(time.Now().Add(time.Second))),
				},
			}),
		},
	})

	// initial syncs -> no live events
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"the_list": {
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 3,
					RequiredState: [][2]string{
						{"m.room.encryption", ""},
						{"m.room.tombstone", ""},
					},
				},
				Sort:   []string{sync3.SortByRecency, sync3.SortByName},
				Ranges: sync3.SliceRanges{{0, 1}},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(
		map[string][]m.RoomMatcher{
			roomID: {
				m.MatchNumLive(0),
			},
		},
	), m.MatchList("the_list", m.MatchV3Count(1), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 0, []string{roomID}),
	)))

	// inject 10 events in batches of 2, 1, 3, 4
	batchSizes := []int{2, 1, 3, 4}
	count := 0
	var completeTimeline []json.RawMessage
	for _, sz := range batchSizes {
		var timeline []json.RawMessage
		for i := 0; i < sz; i++ {
			timeline = append(timeline, testutils.NewEvent(
				t, "m.room.message",
				alice, map[string]interface{}{"body": fmt.Sprintf("Msg %d", count)}, testutils.WithTimestamp(time.Now().Add(time.Minute*time.Duration(1+count))),
			))
			count++
		}
		v2.queueResponse(aliceToken, sync2.SyncResponse{
			Rooms: sync2.SyncRoomsResponse{
				Join: v2JoinTimeline(roomEvents{
					roomID: roomID,
					events: timeline,
				}),
			},
		})
		v2.waitUntilEmpty(t, aliceToken)
		completeTimeline = append(completeTimeline, timeline...)
	}
	res = v3.mustDoV3RequestWithPos(t, aliceToken, res.Pos, sync3.Request{})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(
		map[string][]m.RoomMatcher{
			roomID: {
				m.MatchRoomTimeline(completeTimeline),
				m.MatchNumLive(10),
			},
		},
	))
}
