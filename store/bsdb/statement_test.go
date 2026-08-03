package bsdb

import (
	"testing"

	permtypes "github.com/mocachain/moca/v2/x/permission/types"
)

func TestStatement_Eval(t *testing.T) {
	tests := []struct {
		name      string
		statement *Statement
		action    permtypes.ActionType
		opts      *permtypes.VerifyOptions
		want      permtypes.Effect
	}{
		{
			name:      "nil opts",
			statement: &Statement{},
			action:    permtypes.ACTION_TYPE_ALL,
			opts:      nil,
			want:      permtypes.EFFECT_UNSPECIFIED,
		},
		{
			name:      "empty resource in opts",
			statement: &Statement{Resources: []string{"test_resource"}},
			action:    permtypes.ACTION_TYPE_ALL,
			opts:      &permtypes.VerifyOptions{Resource: ""},
			want:      permtypes.EFFECT_UNSPECIFIED,
		},
		{
			name:      "nil resources in statement",
			statement: &Statement{Resources: nil},
			action:    permtypes.ACTION_TYPE_ALL,
			opts:      &permtypes.VerifyOptions{Resource: "test_resource"},
			want:      permtypes.EFFECT_UNSPECIFIED,
		},
		{
			name: "action matches - deny effect",
			statement: &Statement{
				ActionValue: 1 << int(permtypes.ACTION_UPDATE_BUCKET_INFO),
				Effect:      permtypes.EFFECT_DENY.String(),
				Resources:   []string{"test_resource"},
			},
			action: permtypes.ACTION_UPDATE_BUCKET_INFO,
			opts:   &permtypes.VerifyOptions{Resource: "test_resource"},
			want:   permtypes.EFFECT_DENY,
		},
		{
			name: "action matches - non deny effect",
			statement: &Statement{
				ActionValue: 1 << int(permtypes.ACTION_UPDATE_BUCKET_INFO),
				Effect:      permtypes.EFFECT_ALLOW.String(),
				Resources:   []string{"test_resource"},
			},
			action: permtypes.ACTION_UPDATE_BUCKET_INFO,
			opts:   &permtypes.VerifyOptions{Resource: "test_resource"},
			want:   permtypes.EFFECT_ALLOW,
		},
		{
			name: "action doesn't match",
			statement: &Statement{
				ActionValue: 1 << ActionTypeMap[permtypes.ACTION_UPDATE_BUCKET_INFO],
				Effect:      permtypes.EFFECT_ALLOW.String(),
			},
			action: permtypes.ACTION_DELETE_BUCKET,
			opts:   &permtypes.VerifyOptions{Resource: "test_resource"},
			want:   permtypes.EFFECT_UNSPECIFIED,
		},
		{
			// the previous version of this case set every bit, so it passed on the
			// ACTION_DELETE_BUCKET bit and never exercised ACTION_TYPE_ALL at all
			name: "ACTION_TYPE_ALL matches everything",
			statement: &Statement{
				ActionValue: 1 << ActionTypeMap[permtypes.ACTION_TYPE_ALL],
				Effect:      permtypes.EFFECT_ALLOW.String(),
				Resources:   []string{"test_resource"},
			},
			action: permtypes.ACTION_DELETE_BUCKET,
			opts:   &permtypes.VerifyOptions{Resource: "test_resource"},
			want:   permtypes.EFFECT_ALLOW,
		},
		{
			name: "ACTION_TYPE_ALL denies everything",
			statement: &Statement{
				ActionValue: 1 << ActionTypeMap[permtypes.ACTION_TYPE_ALL],
				Effect:      permtypes.EFFECT_DENY.String(),
				Resources:   []string{"test_resource"},
			},
			action: permtypes.ACTION_GET_OBJECT,
			opts:   &permtypes.VerifyOptions{Resource: "test_resource"},
			want:   permtypes.EFFECT_DENY,
		},
		{
			name: "ACTION_TYPE_ALL deny wins over an unrelated allow bit",
			statement: &Statement{
				ActionValue: 1<<ActionTypeMap[permtypes.ACTION_TYPE_ALL] | 1<<ActionTypeMap[permtypes.ACTION_CREATE_OBJECT],
				Effect:      permtypes.EFFECT_DENY.String(),
				Resources:   []string{"test_resource"},
			},
			action: permtypes.ACTION_DELETE_OBJECT,
			opts:   &permtypes.VerifyOptions{Resource: "test_resource"},
			want:   permtypes.EFFECT_DENY,
		},
		{
			name: "action matches - deny effect",
			statement: &Statement{
				Resources:   []string{"test_resource"},
				ActionValue: int(permtypes.ACTION_UPDATE_BUCKET_INFO),
				Effect:      permtypes.EFFECT_ALLOW.String(),
			},
			action: permtypes.ACTION_DELETE_BUCKET,
			opts:   &permtypes.VerifyOptions{Resource: "non_matching_resource"},
			want:   permtypes.EFFECT_UNSPECIFIED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.statement.Eval(tt.action, tt.opts); got != tt.want {
				t.Errorf("Statement.Eval() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestStatement_EvalWithInvalidResourcePattern covers policy resources that are not
// valid regular expressions. The resource strings come from on-chain policies, so a
// user picks them; an unparseable one must not take the permission evaluation down.
func TestStatement_EvalWithInvalidResourcePattern(t *testing.T) {
	tests := []struct {
		name      string
		statement *Statement
		want      permtypes.Effect
	}{
		{
			name: "only an invalid pattern, nothing can match",
			statement: &Statement{
				Resources:   []string{"["},
				ActionValue: 1 << ActionTypeMap[permtypes.ACTION_UPDATE_BUCKET_INFO],
				Effect:      permtypes.EFFECT_ALLOW.String(),
			},
			want: permtypes.EFFECT_UNSPECIFIED,
		},
		{
			name: "an invalid pattern does not hide a valid one after it",
			statement: &Statement{
				Resources:   []string{"a(b", "test_resource"},
				ActionValue: 1 << ActionTypeMap[permtypes.ACTION_UPDATE_BUCKET_INFO],
				Effect:      permtypes.EFFECT_ALLOW.String(),
			},
			want: permtypes.EFFECT_ALLOW,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.statement.Eval(permtypes.ACTION_UPDATE_BUCKET_INFO,
				&permtypes.VerifyOptions{Resource: "test_resource"})
			if got != tt.want {
				t.Errorf("Statement.Eval() = %v, want %v", got, tt.want)
			}
		})
	}
}
