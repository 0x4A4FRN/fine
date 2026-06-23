package executor

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

type mockMemberAPI struct {
	members   map[string]*discordgo.Member
	roles     []*discordgo.Role
	guild     *discordgo.Guild
	botID     string
	memberErr error
	rolesErr  error
	guildErr  error
}

func (m *mockMemberAPI) GuildMember(guildID, userID string, _ ...discordgo.RequestOption) (*discordgo.Member, error) {
	if m.memberErr != nil {
		return nil, m.memberErr
	}
	return m.members[userID], nil
}
func (m *mockMemberAPI) GuildRoles(guildID string, _ ...discordgo.RequestOption) ([]*discordgo.Role, error) {
	if m.rolesErr != nil {
		return nil, m.rolesErr
	}
	return m.roles, nil
}
func (m *mockMemberAPI) Guild(guildID string, _ ...discordgo.RequestOption) (*discordgo.Guild, error) {
	if m.guildErr != nil {
		return nil, m.guildErr
	}
	return m.guild, nil
}
func (m *mockMemberAPI) BotUserID() string { return m.botID }

func newMockAPI(actorID string, actorPerms int64, botID string) *mockMemberAPI {
	roleID := "role-1"
	return &mockMemberAPI{
		members: map[string]*discordgo.Member{
			actorID: {Roles: []string{roleID}, User: &discordgo.User{ID: actorID}},
		},
		roles: []*discordgo.Role{{ID: roleID, Permissions: actorPerms, Position: 5}},
		guild: &discordgo.Guild{ID: "guild-1", OwnerID: "owner-1"},
		botID: botID,
	}
}

const (
	testActorID  = "111111111111111111"
	testTargetID = "222222222222222222"
	testBotID    = "333333333333333333"
	testOwnerID  = "owner-1"
)

func alwaysAllow(_ string, _ int64) bool { return true }
func alwaysDeny(_ string, _ int64) bool  { return false }

func TestGate_PermissionDenied(t *testing.T) {
	api := newMockAPI(testActorID, 0, testBotID)
	action := Action{GuildID: "guild-1", ActorID: testActorID, ChannelID: "chan-1"}
	msg := gate(api, nil, alwaysDeny, "ban", action, testTargetID, false, false, false)
	if msg == "" {
		t.Fatal("expected denial for no permission")
	}
}

func TestGate_PermissionAllowed(t *testing.T) {
	api := newMockAPI(testActorID, discordgo.PermissionBanMembers, testBotID)
	api.members[testTargetID] = &discordgo.Member{Roles: []string{"role-2"}, User: &discordgo.User{ID: testTargetID}}
	api.roles = append(api.roles, &discordgo.Role{ID: "role-2", Permissions: 0, Position: 1})
	action := Action{GuildID: "guild-1", ActorID: testActorID, ChannelID: "chan-1"}
	msg := gate(api, nil, alwaysAllow, "ban", action, testTargetID, false, false, false)
	if msg != "" {
		t.Fatalf("expected empty (allowed), got %q", msg)
	}
}

func TestGate_SelfProtection_ActorTargetingSelf(t *testing.T) {
	api := newMockAPI(testActorID, discordgo.PermissionAdministrator, testBotID)
	action := Action{GuildID: "guild-1", ActorID: testActorID, ChannelID: "chan-1"}
	msg := gate(api, nil, alwaysAllow, "ban", action, testActorID, false, false, false)
	if msg == "" {
		t.Fatal("expected self-protection denial")
	}
}

func TestGate_SelfProtection_BotTargeted(t *testing.T) {
	api := newMockAPI(testActorID, discordgo.PermissionAdministrator, testBotID)
	action := Action{GuildID: "guild-1", ActorID: testActorID, ChannelID: "chan-1"}
	msg := gate(api, nil, alwaysAllow, "ban", action, testBotID, false, false, false)
	if msg == "" {
		t.Fatal("expected bot self-protection denial")
	}
}

func TestGate_SelfProtection_OwnerTargeted(t *testing.T) {
	api := newMockAPI(testActorID, discordgo.PermissionAdministrator, testBotID)
	api.members[testOwnerID] = &discordgo.Member{Roles: []string{"role-2"}, User: &discordgo.User{ID: testOwnerID}}
	api.roles = append(api.roles, &discordgo.Role{ID: "role-2", Permissions: 0, Position: 1})
	action := Action{GuildID: "guild-1", ActorID: testActorID, ChannelID: "chan-1"}
	msg := gate(api, nil, alwaysAllow, "ban", action, testOwnerID, false, false, false)
	if msg == "" {
		t.Fatal("expected owner self-protection denial")
	}
}

func TestGate_SkipSelfChecks_AllowsSelfTarget(t *testing.T) {
	api := newMockAPI(testActorID, discordgo.PermissionManageNicknames, testBotID)
	action := Action{GuildID: "guild-1", ActorID: testActorID, ChannelID: "chan-1"}
	msg := gate(api, nil, alwaysAllow, "nickname", action, testActorID, false, true, false)
	if msg != "" {
		t.Fatalf("expected empty (self-target with skipSelfChecks), got %q", msg)
	}
}

func TestGate_InvalidSnowflake(t *testing.T) {
	api := newMockAPI(testActorID, discordgo.PermissionAdministrator, testBotID)
	action := Action{GuildID: "guild-1", ActorID: testActorID, ChannelID: "chan-1"}
	msg := gate(api, nil, alwaysAllow, "ban", action, "not-a-snowflake", false, false, false)
	if msg == "" {
		t.Fatal("expected denial for invalid snowflake")
	}
}

func TestGate_Hierarchy_ActorBelowTarget(t *testing.T) {
	api := newMockAPI(testActorID, discordgo.PermissionAdministrator, testBotID)
	api.members[testTargetID] = &discordgo.Member{Roles: []string{"role-high"}, User: &discordgo.User{ID: testTargetID}}
	api.roles = append(api.roles, &discordgo.Role{ID: "role-high", Permissions: 0, Position: 10})
	action := Action{GuildID: "guild-1", ActorID: testActorID, ChannelID: "chan-1"}
	msg := gate(api, nil, alwaysAllow, "ban", action, testTargetID, false, false, false)
	if msg == "" {
		t.Fatal("expected hierarchy denial")
	}
}

func TestGate_Hierarchy_ActorAboveTarget(t *testing.T) {
	api := newMockAPI(testActorID, discordgo.PermissionAdministrator, testBotID)
	api.members[testTargetID] = &discordgo.Member{Roles: []string{"role-low"}, User: &discordgo.User{ID: testTargetID}}
	api.roles = append(api.roles, &discordgo.Role{ID: "role-low", Permissions: 0, Position: 1})
	action := Action{GuildID: "guild-1", ActorID: testActorID, ChannelID: "chan-1"}
	msg := gate(api, nil, alwaysAllow, "ban", action, testTargetID, false, false, false)
	if msg != "" {
		t.Fatalf("expected empty (actor above target), got %q", msg)
	}
}

func TestGate_SelfTargetSkipsHierarchy(t *testing.T) {
	api := newMockAPI(testActorID, discordgo.PermissionManageNicknames, testBotID)
	action := Action{GuildID: "guild-1", ActorID: testActorID, ChannelID: "chan-1"}
	msg := gate(api, nil, alwaysAllow, "nickname", action, testActorID, false, true, false)
	if msg != "" {
		t.Fatalf("expected empty (self-target skips hierarchy), got %q", msg)
	}
}

func TestGate_EmptyTargetID_AllowsSkipUserChecks(t *testing.T) {
	api := newMockAPI(testActorID, discordgo.PermissionManageMessages, testBotID)
	action := Action{GuildID: "guild-1", ActorID: testActorID, ChannelID: "chan-1"}
	msg := gate(api, nil, alwaysAllow, "purge", action, "", true, false, false)
	if msg != "" {
		t.Fatalf("expected empty (empty target + skipUserChecks), got %q", msg)
	}
}

func TestGate_GuildMemberLookupFails(t *testing.T) {
	api := newMockAPI(testActorID, 0, testBotID)
	api.memberErr = &discordgo.RESTError{}
	action := Action{GuildID: "guild-1", ActorID: testActorID, ChannelID: "chan-1"}
	msg := gate(api, nil, alwaysAllow, "ban", action, testTargetID, false, false, false)
	if msg == "" {
		t.Fatal("expected denial when member lookup fails")
	}
}

func TestGate_GuildRolesLookupFails(t *testing.T) {
	api := newMockAPI(testActorID, discordgo.PermissionAdministrator, testBotID)
	api.rolesErr = &discordgo.RESTError{}
	action := Action{GuildID: "guild-1", ActorID: testActorID, ChannelID: "chan-1"}
	msg := gate(api, nil, alwaysAllow, "ban", action, testTargetID, false, false, false)
	if msg == "" {
		t.Fatal("expected denial when roles lookup fails")
	}
}
