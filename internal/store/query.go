package store

// 本文件集中放置状态查询辅助函数，避免各层重复线性扫描逻辑。

func UserByID(s *State, id string) *User {
	for i := range s.Users {
		if s.Users[i].ID == id {
			return &s.Users[i]
		}
	}
	return nil
}

func UserByName(s *State, name string) *User {
	for i := range s.Users {
		if s.Users[i].Username == name {
			return &s.Users[i]
		}
	}
	return nil
}

func PluginByID(s *State, id string) *Plugin {
	for i := range s.Plugins {
		if s.Plugins[i].ID == id {
			return &s.Plugins[i]
		}
	}
	return nil
}

func BucketByName(s *State, name string) *Bucket {
	for i := range s.Buckets {
		if s.Buckets[i].Name == name {
			return &s.Buckets[i]
		}
	}
	return nil
}

func APIKeyByID(s *State, id string) *APIKey {
	for i := range s.APIKeys {
		if s.APIKeys[i].ID == id {
			return &s.APIKeys[i]
		}
	}
	return nil
}

func APIKeyByAccess(s *State, access string) *APIKey {
	for i := range s.APIKeys {
		if s.APIKeys[i].AccessKey == access {
			return &s.APIKeys[i]
		}
	}
	return nil
}

func ObjectByKey(s *State, bucket, key string) *Object {
	for i := range s.Objects {
		if s.Objects[i].Bucket == bucket && s.Objects[i].ObjectKey == key {
			return &s.Objects[i]
		}
	}
	return nil
}

func AlertByID(s *State, id string) *Alert {
	for i := range s.Alerts {
		if s.Alerts[i].ID == id {
			return &s.Alerts[i]
		}
	}
	return nil
}
