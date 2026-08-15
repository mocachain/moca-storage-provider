package bsdb

import (
	"regexp"

	permtypes "github.com/evmos/evmos/v12/x/permission/types"

	"github.com/mocachain/moca-storage-provider/pkg/log"
)

// Eval is used to evaluate the execution results of statement policies.
func (s *Statement) Eval(action permtypes.ActionType, opts *permtypes.VerifyOptions) permtypes.Effect {
	// If 'resource' is not nil, it implies that the user intends to access a sub-resource, which would
	// be specified in 's.Resources'. Therefore, if the sub-resource in the statement is nil, we will ignore this statement.
	if opts != nil && opts.Resource != "" && s != nil && s.Resources == nil {
		return permtypes.EFFECT_UNSPECIFIED
	}
	// If 'resource' is not nil, and 's.Resource' is also not nil, it indicates that we should verify whether
	// the resource that the user intends to access matches any items in 's.Resource'
	if opts != nil && opts.Resource != "" && s != nil && s.Resources != nil {
		isMatch := false
		for _, res := range s.Resources {
			// the resource patterns come from on-chain policies, so a user chooses
			// them; one that is not a valid regular expression matches nothing
			// instead of taking the permission evaluation down
			reg, err := regexp.Compile(res)
			if err != nil {
				log.Errorw("failed to compile policy resource pattern", "resource", res, "error", err)
				continue
			}
			if reg.MatchString(opts.Resource) {
				isMatch = true
				break
			}
		}
		if !isMatch {
			return permtypes.EFFECT_UNSPECIFIED
		}
	}

	// convert action bitmap to action list. The bit position and the action type are
	// two different numbers - ACTION_TYPE_ALL is 99 and sits in bit 0 - so the action
	// has to come from the map key, not from the bit index.
	actions := make([]permtypes.ActionType, 0)
	for actionType, bit := range ActionTypeMap {
		if s.ActionValue&(1<<bit) == 1<<bit {
			actions = append(actions, actionType)
		}
	}

	for _, act := range actions {
		if act == action || act == permtypes.ACTION_TYPE_ALL {
			// Action matched, if effect is deny, then return deny
			if s.Effect == permtypes.EFFECT_DENY.String() {
				return permtypes.EFFECT_DENY
			}
			return permtypes.Effect(permtypes.Effect_value[s.Effect])
		}
	}

	return permtypes.EFFECT_UNSPECIFIED
}
