package types

import (
	"encoding/base64"
	"encoding/xml"

	storageTypes "github.com/evmos/evmos/v12/x/storage/types"
)

func (m GfSpListObjectsByBucketNameResponse) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	type Alias GfSpListObjectsByBucketNameResponse
	// Create a new struct with Base64-encoded Checksums field
	responseAlias := Alias(m)
	for _, o := range responseAlias.Objects {
		for i, c := range o.ObjectInfo.Checksums {
			o.ObjectInfo.Checksums[i] = []byte(base64.StdEncoding.EncodeToString(c))
		}
	}
	return e.EncodeElement(responseAlias, start)
}

func (m GfSpGetObjectMetaResponse) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	type Alias GfSpGetObjectMetaResponse
	// Create a new struct with Base64-encoded Checksums field
	responseAlias := Alias(m)
	o := responseAlias.Object
	if o != nil && o.ObjectInfo != nil && o.ObjectInfo.Checksums != nil {
		for i, c := range o.ObjectInfo.Checksums {
			o.ObjectInfo.Checksums[i] = []byte(base64.StdEncoding.EncodeToString(c))
		}
	}
	return e.EncodeElement(responseAlias, start)
}

type GroupEntry struct {
	Id    uint64
	Value *Group
}

func (m GfSpListGroupsByIDsResponse) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if len(m.Groups) == 0 {
		return nil
	}

	err := e.EncodeToken(start)
	if err != nil {
		return err
	}

	for k, v := range m.Groups {
		e.Encode(GroupEntry{Id: k, Value: v})
	}

	return e.EncodeToken(start.End())
}

// GroupInfoXML is a helper struct for XML marshaling GroupInfo with proper ID serialization
type GroupInfoXML struct {
	Owner      string `xml:"owner"`
	GroupName  string `xml:"group_name"`
	SourceType string `xml:"source_type"`
	Id         string `xml:"id"` // Serialize as string instead of cosmossdk.io/math.Uint
	Extra      string `xml:"extra"`
	Tags       *storageTypes.ResourceTags `xml:"tags,omitempty"`
}

// MarshalXML custom marshaler for GroupMember to properly serialize the group ID
func (m *GroupMember) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	// Define auxiliary struct without the Group field to avoid recursion
	type GroupMemberNoGroup struct {
		AccountId      string `xml:"account_id,omitempty"`
		Operator       string `xml:"operator,omitempty"`
		CreateAt       int64  `xml:"create_at,omitempty"`
		CreateTime     int64  `xml:"create_time,omitempty"`
		UpdateAt       int64  `xml:"update_at,omitempty"`
		UpdateTime     int64  `xml:"update_time,omitempty"`
		Removed        bool   `xml:"removed,omitempty"`
		ExpirationTime int64  `xml:"expiration_time,omitempty"`
	}
	
	aux := struct {
		GroupXML *GroupInfoXML     `xml:"Group,omitempty"`
		*GroupMemberNoGroup
	}{
		GroupMemberNoGroup: &GroupMemberNoGroup{
			AccountId:      m.AccountId,
			Operator:       m.Operator,
			CreateAt:       m.CreateAt,
			CreateTime:     m.CreateTime,
			UpdateAt:       m.UpdateAt,
			UpdateTime:     m.UpdateTime,
			Removed:        m.Removed,
			ExpirationTime: m.ExpirationTime,
		},
	}
	
	// Convert GroupInfo to GroupInfoXML with string ID
	if m.Group != nil {
		aux.GroupXML = &GroupInfoXML{
			Owner:      m.Group.Owner,
			GroupName:  m.Group.GroupName,
			SourceType: m.Group.SourceType.String(),
			Id:         m.Group.Id.String(), // Convert Uint to string
			Extra:      m.Group.Extra,
			Tags:       m.Group.Tags,
		}
	}
	
	return e.EncodeElement(aux, start)
}

type ObjectEntry struct {
	Id    uint64
	Value *Object
}

func (m GfSpListObjectsByIDsResponse) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if len(m.Objects) == 0 {
		return nil
	}

	err := e.EncodeToken(start)
	if err != nil {
		return err
	}

	for k, o := range m.Objects {
		if o != nil && o.ObjectInfo != nil && o.ObjectInfo.Checksums != nil {
			for i, c := range o.ObjectInfo.Checksums {
				o.ObjectInfo.Checksums[i] = []byte(base64.StdEncoding.EncodeToString(c))
			}
		}
		e.Encode(ObjectEntry{Id: k, Value: o})
	}

	return e.EncodeToken(start.End())
}

type BucketEntry struct {
	Id    uint64
	Value *Bucket
}

func (m GfSpListBucketsByIDsResponse) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if len(m.Buckets) == 0 {
		return nil
	}

	err := e.EncodeToken(start)
	if err != nil {
		return err
	}

	for k, v := range m.Buckets {
		e.Encode(BucketEntry{Id: k, Value: v})
	}

	return e.EncodeToken(start.End())
}
