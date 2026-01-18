package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/yukimochi/Activity-Relay/delaymetrics"
	"github.com/yukimochi/Activity-Relay/models"
)

func handleWebfinger(writer http.ResponseWriter, request *http.Request) {
	queriedResource := request.URL.Query()["resource"]
	if request.Method != "GET" || len(queriedResource) == 0 {
		writer.WriteHeader(400)
		writer.Write(nil)
	} else {
		queriedSubject := queriedResource[0]
		for _, webfingerResource := range WebfingerResources {
			if queriedSubject == webfingerResource.Subject {
				webfinger, err := json.Marshal(&webfingerResource)
				if err != nil {
					logrus.Fatal("Failed to marshal webfinger resource : ", err.Error())
					writer.WriteHeader(500)
					writer.Write(nil)
					return
				}
				writer.Header().Add("Content-Type", "application/json")
				writer.WriteHeader(200)
				writer.Write(webfinger)
				return
			}
		}
		writer.WriteHeader(404)
		writer.Write(nil)
	}
}

func handleNodeinfoLink(writer http.ResponseWriter, request *http.Request) {
	if request.Method != "GET" {
		writer.WriteHeader(400)
		writer.Write(nil)
	} else {
		nodeinfoLinks, err := json.Marshal(&Nodeinfo.NodeinfoLinks)
		if err != nil {
			logrus.Fatal("Failed to marshal nodeinfo links : ", err.Error())
			writer.WriteHeader(500)
			writer.Write(nil)
			return
		}
		writer.Header().Add("Content-Type", "application/json")
		writer.WriteHeader(200)
		writer.Write(nodeinfoLinks)
	}
}

func handleNodeinfo(writer http.ResponseWriter, request *http.Request) {
	if request.Method != "GET" {
		writer.WriteHeader(400)
		writer.Write(nil)
	} else {
		// Count both subscribers and followers (Akkoma/Pleroma use follower style)
		userTotal := len(RelayState.Subscribers) + len(RelayState.Followers)
		Nodeinfo.Nodeinfo.Usage.Users.Total = userTotal
		Nodeinfo.Nodeinfo.Usage.Users.ActiveMonth = userTotal
		Nodeinfo.Nodeinfo.Usage.Users.ActiveHalfyear = userTotal
		nodeinfo, err := json.Marshal(&Nodeinfo.Nodeinfo)
		if err != nil {
			logrus.Fatal("Failed to marshal nodeinfo : ", err.Error())
			writer.WriteHeader(500)
			writer.Write(nil)
			return
		}
		writer.Header().Add("Content-Type", "application/json")
		writer.WriteHeader(200)
		writer.Write(nodeinfo)
	}
}

func handleRelayActor(writer http.ResponseWriter, request *http.Request) {
	if request.Method == "GET" {
		relayActor, err := json.Marshal(&RelayActor)
		if err != nil {
			logrus.Fatal("Failed to marshal relay actor : ", err.Error())
			writer.WriteHeader(500)
			writer.Write(nil)
			return
		}
		writer.Header().Add("Content-Type", "application/activity+json")
		writer.WriteHeader(200)
		writer.Write(relayActor)
	} else {
		writer.WriteHeader(400)
		writer.Write(nil)
	}
}

func handleInbox(writer http.ResponseWriter, request *http.Request, activityDecoder func(*http.Request) (*models.Activity, *models.Actor, []byte, error)) {
	switch request.Method {
	case "POST":
		receivedAt := time.Now()
		// Increment inbox counter for statistics
		IncrementInboxCount()

		activity, actor, body, err := activityDecoder(request)
		if err != nil {
			writer.WriteHeader(400)
			writer.Write(nil)
		} else {
			actorID, _ := url.Parse(activity.Actor)

			// Record delay metrics for federation delay analysis
			recordDelayMetrics(activity, actorID, receivedAt)

			switch {
			case contains(activity.To, "https://www.w3.org/ns/activitystreams#Public"), contains(activity.Cc, "https://www.w3.org/ns/activitystreams#Public"):
				// Mastodon Traditional Style (Activity Transfer)
				switch activity.Type {
				case "Create", "Update", "Delete", "Move":
					err = executeRelayActivity(activity, actor, body)
					if err != nil {
						writer.WriteHeader(401)
						writer.Write([]byte(err.Error()))

						return
					}
					writer.WriteHeader(202)
					writer.Write(nil)
				default:
					writer.WriteHeader(202)
					writer.Write(nil)
				}
			case contains(activity.To, RelayActor.ID), contains(activity.Cc, RelayActor.ID):
				// LitePub Relay Style
				fallthrough
			case isToMyFollower(activity.To), isToMyFollower(activity.Cc):
				// LitePub Relay Style
				switch activity.Type {
				case "Follow":
					err = executeFollowing(activity, actor)
					if err != nil {
						executeRejectRequest(activity, actor, err)
					}
					writer.WriteHeader(202)
					writer.Write(nil)
				case "Undo":
					innerActivity, err := activity.UnwrapInnerActivity()
					if err != nil {
						writer.WriteHeader(202)
						writer.Write(nil)

						return
					}
					switch innerActivity.Type {
					case "Follow":
						err = executeUnfollowing(innerActivity, actor)
						if err != nil {
							executeRejectRequest(activity, actor, err)
						}
						writer.WriteHeader(202)
						writer.Write(nil)
					default:
						writer.WriteHeader(202)
						writer.Write(nil)
					}
				case "Accept":
					innerActivity, err := activity.UnwrapInnerActivity()
					if err != nil {
						writer.WriteHeader(202)
						writer.Write(nil)

						return
					}
					switch innerActivity.Type {
					case "Follow":
						finalizeMutuallyFollow(innerActivity, actor, activity.Type)
						writer.WriteHeader(202)
						writer.Write(nil)
					default:
						writer.WriteHeader(202)
						writer.Write(nil)
					}
				case "Reject":
					innerActivity, err := activity.UnwrapInnerActivity()
					if err != nil {
						writer.WriteHeader(202)
						writer.Write(nil)

						return
					}
					switch innerActivity.Type {
					case "Follow":
						finalizeMutuallyFollow(innerActivity, actor, activity.Type)
						writer.WriteHeader(202)
						writer.Write(nil)
					default:
						writer.WriteHeader(202)
						writer.Write(nil)
					}
				case "Announce":
					if !isActorSubscribersOrFollowers(actorID) {
						err = errors.New("to use the relay service, please follow in advance")
						writer.WriteHeader(401)
						writer.Write([]byte(err.Error()))

						return
					}
					switch innerObject := activity.Object.(type) {
					case string:
						origActivity, origActor, err := fetchOriginalActivityFromURL(innerObject)
						if err != nil {
							logrus.Debug("Failed Announce Activity : ", activity.Actor)
							writer.WriteHeader(400)
							writer.Write([]byte(err.Error()))

							return
						}
						executeAnnounceActivity(origActivity, origActor)
					default:
						logrus.Debug("Skipped Announce Activity : ", activity.Actor)
					}
					writer.WriteHeader(202)
					writer.Write(nil)
				default:
					writer.WriteHeader(202)
					writer.Write(nil)
				}
			default:
				// Follow, Unfollow Only
				switch activity.Type {
				case "Follow":
					err = executeFollowing(activity, actor)
					if err != nil {
						executeRejectRequest(activity, actor, err)
					}
					writer.WriteHeader(202)
					writer.Write(nil)
				case "Undo":
					innerActivity, err := activity.UnwrapInnerActivity()
					if err != nil {
						writer.WriteHeader(202)
						writer.Write(nil)

						return
					}
					switch innerActivity.Type {
					case "Follow":
						err = executeUnfollowing(innerActivity, actor)
						if err != nil {
							executeRejectRequest(activity, actor, err)
						}
						writer.WriteHeader(202)
						writer.Write(nil)
					default:
						writer.WriteHeader(202)
						writer.Write(nil)
					}
				default:
					writer.WriteHeader(202)
					writer.Write(nil)
				}
			}
		}
	default:
		writer.WriteHeader(405)
		writer.Write(nil)
	}
}

// handleAdminUnfollow handles unfollow requests from the admin API
// POST /api/admin/unfollow
// Body: {"domain": "example.com"}
// Response: {"success": true, "type": "subscriber"|"follower"} or {"error": "..."}
func handleAdminUnfollow(writer http.ResponseWriter, request *http.Request) {
	if request.Method != "POST" {
		writer.WriteHeader(405)
		writer.Write(nil)
		return
	}

	// Parse request body
	var req struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(request.Body).Decode(&req); err != nil {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(400)
		json.NewEncoder(writer).Encode(map[string]string{"error": "invalid request body"})
		return
	}

	if req.Domain == "" {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(400)
		json.NewEncoder(writer).Encode(map[string]string{"error": "domain required"})
		return
	}

	// Check if subscriber
	subscriber := RelayState.SelectSubscriber(req.Domain)
	if subscriber != nil {
		// Send Reject activity to subscriber
		activity := models.Activity{
			Context: []string{"https://www.w3.org/ns/activitystreams", "https://w3id.org/security/v1"},
			ID:      subscriber.ActivityID,
			Actor:   subscriber.ActorID,
			Type:    "Follow",
			Object:  "https://www.w3.org/ns/activitystreams#Public",
		}
		resp := activity.GenerateReply(RelayActor, activity, "Reject")
		jsonData, _ := json.Marshal(&resp)
		enqueueRegisterActivity(subscriber.InboxURL, jsonData)

		// Remove from state
		RelayState.DelSubscriber(subscriber.Domain)

		logrus.Info("Admin unfollow sent for subscriber: ", req.Domain)

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(200)
		json.NewEncoder(writer).Encode(map[string]interface{}{"success": true, "type": "subscriber"})
		return
	}

	// Check if follower
	follower := RelayState.SelectFollower(req.Domain)
	if follower != nil {
		// Send Reject activity to follower
		activity := models.Activity{
			Context: []string{"https://www.w3.org/ns/activitystreams", "https://w3id.org/security/v1"},
			ID:      follower.ActivityID,
			Actor:   follower.ActorID,
			Type:    "Follow",
			Object:  RelayActor.ID,
		}
		resp := activity.GenerateReply(RelayActor, activity, "Reject")
		jsonData, _ := json.Marshal(&resp)
		enqueueRegisterActivity(follower.InboxURL, jsonData)

		// Remove from state
		RelayState.DelFollower(follower.Domain)

		logrus.Info("Admin unfollow sent for follower: ", req.Domain)

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(200)
		json.NewEncoder(writer).Encode(map[string]interface{}{"success": true, "type": "follower"})
		return
	}

	// Domain not found
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(404)
	json.NewEncoder(writer).Encode(map[string]string{"error": "Domain not found in subscribers or followers"})
}

// recordDelayMetrics extracts createdAt from activity and records the delay
func recordDelayMetrics(activity *models.Activity, actorID *url.URL, receivedAt time.Time) {
	if activity == nil || actorID == nil {
		return
	}

	// Extract createdAt from the activity or its object
	var createdAtStr string
	var objectID string

	// First, try to get published from the activity itself
	if activity.Published != "" {
		createdAtStr = activity.Published
		logrus.Debugf("DelayMetrics: Found published in activity: %s", createdAtStr)
	}

	// Then, try to get from the activity object
	switch obj := activity.Object.(type) {
	case map[string]interface{}:
		if createdAtStr == "" {
			if published, ok := obj["published"].(string); ok {
				createdAtStr = published
				logrus.Debugf("DelayMetrics: Found published in object: %s", createdAtStr)
			}
		}
		if id, ok := obj["id"].(string); ok {
			objectID = id
		}
	case string:
		objectID = obj
	}

	// If still no createdAt, log and skip
	if createdAtStr == "" {
		logrus.Debugf("DelayMetrics: No published timestamp found for %s from %s (type: %s)", activity.ID, actorID.Host, activity.Type)
		return
	}

	if objectID == "" {
		objectID = activity.ID
	}

	// Parse createdAt
	var createdAt time.Time
	var err error

	// Try common ActivityPub date formats
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
	}

	for _, format := range formats {
		createdAt, err = time.Parse(format, createdAtStr)
		if err == nil {
			break
		}
	}

	if err != nil {
		logrus.Debugf("Failed to parse createdAt: %s", createdAtStr)
		return
	}

	// Calculate delay
	delaySeconds := receivedAt.Sub(createdAt).Seconds()

	// Skip if delay is negative (clock skew) or unreasonably large
	if delaySeconds < 0 || delaySeconds > 86400 { // Max 24 hours
		return
	}

	// Record the delay
	record := delaymetrics.DelayRecord{
		NoteID:       objectID,
		CreatedAt:    createdAt,
		ReceivedAt:   receivedAt,
		DelaySeconds: delaySeconds,
		InstanceHost: actorID.Host,
	}

	err = delaymetrics.RecordDelay(record)
	if err != nil {
		logrus.Debugf("Failed to record delay metrics: %v", err)
	}
}
