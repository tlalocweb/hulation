package apiobjects

import (
	"fmt"

	"github.com/google/uuid"
	loglib "github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/store/common"
	filterlib "github.com/tlalocweb/hulation/pkg/utils/filter"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var userLog *loglib.TaggedLogger

func init() {
	userLog = loglib.GetTaggedLogger("USRUTIL", "User utilities")
}

// Store a user object in storage
// Usually CreateOrOverwriteUserByIdentity should be used - as it will prevent collisions
func StoreUser(user *User, storage common.Storage) (err error) {
	user.UpdatedAt = timestamppb.Now()

	userobj := SerWrapUser(user)
	if err != nil {
		return fmt.Errorf("error wrapping user: %v", err)
	}

	serialdata, err := userobj.GetSerialData()

	if err != nil {
		return fmt.Errorf("error getting serial data: %v", err)
	}

	err = storage.Put(uconfig.Storage.Join(uconfig.UsersIndexBaseKey, user.Uuid), serialdata, common.OptionDebuglevel(uconfig.DebugLevel))
	if err != nil {
		return fmt.Errorf("error storing user: %v", err)
	}

	return nil
}

type UserMutator func(user *User, isnew bool) (err error, dodel bool)

// convenience function to update a user through MutateObject. It will call mutatorfunc with either a new user or an existing one
// allowing the caller to make whatever changes desired. If the func returns a dodel == true, then the user will be deleted
// and the function will return nil.
func CreateOrOverwriteUserByIdentity(identity string, provider string, mutatorfunc UserMutator) (err error) {
	mutator := func(key string, obj *common.Unwrappable) (err error, dodel bool) {
		err = obj.Unwrap()
		if err != nil {
			err = fmt.Errorf("error unwrapping user: %v", err)
			return
		}

		if obj.Obj() == nil {
			user := &User{}
			user.ExternalIdentity = identity
			user.UpdatedAt = timestamppb.Now()
			user.CreatedAt = timestamppb.Now()
			user.Uuid = uuid.New().String()
			// Set provider
			if provider != "" {
				user.AuthProvider = &AuthProviderRef{
					ProviderName: provider,
				}
			}
			err, dodel = mutatorfunc(user, true)
			if err != nil {
				err = fmt.Errorf("error mutating user - mutatorfunc: %v", err)
				return
			}
			if dodel {
				return
			}
			wrapper := SerWrapUser(user)
			wrapper.SetCollectionBase(uconfig.UsersIndexBaseKey)
			wrapper.SetCollectionKey(user.Uuid)
			wrapper.Indexes(common.UpdateIndex(USER_BY_IDENTITY_INDEX, ProviderIdentityString(user.ExternalIdentity, user.AuthProvider)))
			if user.Email != "" {
				wrapper.Indexes(common.UpdateIndex(USER_BY_EMAIL_INDEX, user.Email))
			}
			// Add validation token indexes for all pending validations
			for _, pv := range user.PendingValidation {
				if pv != nil && pv.ValidationToken != "" {
					wrapper.Indexes(common.UpdateIndex(USER_BY_VALIDATION_TOKEN_INDEX, pv.ValidationToken))
				}
			}
			// Add password reset token index if present
			if user.PasswordResetToken != "" {
				wrapper.Indexes(common.UpdateIndex(USER_BY_PASSWORD_RESET_TOKEN_INDEX, user.PasswordResetToken))
			}
			err = obj.Rewrap(wrapper)
			if err != nil {
				err = fmt.Errorf("error rewrapping user: %v", err)
				return
			}
		} else {
			// update the user
			user := obj.Obj().(*User)
			user.UpdatedAt = timestamppb.Now()
			err, dodel = mutatorfunc(user, false)
			if err != nil {
				err = fmt.Errorf("error mutating user - mutatorfunc: %v", err)
				return
			}
			wrapper := SerWrapUser(user)
			wrapper.SetCollectionBase(uconfig.UsersIndexBaseKey)
			wrapper.SetCollectionKey(user.Uuid)
			wrapper.Indexes(common.UpdateIndex(USER_BY_IDENTITY_INDEX, ProviderIdentityString(user.ExternalIdentity, user.AuthProvider)))
			if user.Email != "" {
				wrapper.Indexes(common.UpdateIndex(USER_BY_EMAIL_INDEX, user.Email))
			}
			// Add validation token indexes for all pending validations
			for _, pv := range user.PendingValidation {
				if pv != nil && pv.ValidationToken != "" {
					wrapper.Indexes(common.UpdateIndex(USER_BY_VALIDATION_TOKEN_INDEX, pv.ValidationToken))
				}
			}
			// Add password reset token index if present
			if user.PasswordResetToken != "" {
				wrapper.Indexes(common.UpdateIndex(USER_BY_PASSWORD_RESET_TOKEN_INDEX, user.PasswordResetToken))
			}
			err = obj.Rewrap(wrapper)
			if err != nil {
				err = fmt.Errorf("error rewrapping user: %v", err)
				return
			}
		}
		return
	}

	providerRef := &AuthProviderRef{}
	if provider != "" {
		providerRef.ProviderName = provider
	}
	err = uconfig.Storage.MutateObjectFromIndex(uconfig.UsersIndexBaseKey, ProviderIdentityString(identity, providerRef),
		USER_BY_IDENTITY_INDEX, mutator, common.OptionDebuglevel(uconfig.DebugLevel), common.OptionOrCreate())
	if err != nil {
		err = fmt.Errorf("Error mutating object: %v", err)
	}
	return err
}

func CreateOrFailIfExistsByIdentity(identity string, provider string, mutatorfunc UserMutator) (exists bool, err error) {
	mutator := func(key string, obj *common.Unwrappable) (err error, dodel bool) {
		err = obj.Unwrap()
		if err != nil {
			err = fmt.Errorf("error unwrapping user: %v", err)
			return
		}
		if obj.Obj() == nil {
			user := &User{}
			user.ExternalIdentity = identity
			user.UpdatedAt = timestamppb.Now()
			user.CreatedAt = timestamppb.Now()
			user.Uuid = uuid.New().String()
			// Set provider
			if provider != "" {
				user.AuthProvider = &AuthProviderRef{
					ProviderName: provider,
				}
			}
			err, dodel = mutatorfunc(user, true)
			if err != nil {
				err = fmt.Errorf("error mutating user - mutatorfunc: %v", err)
				return
			}
			if dodel {
				return
			}
			wrapper := SerWrapUser(user)
			wrapper.SetCollectionBase(uconfig.UsersIndexBaseKey)
			wrapper.SetCollectionKey(user.Uuid)
			wrapper.Indexes(common.UpdateIndex(USER_BY_IDENTITY_INDEX, ProviderIdentityString(user.ExternalIdentity, user.AuthProvider)))
			if user.Email != "" {
				wrapper.Indexes(common.UpdateIndex(USER_BY_EMAIL_INDEX, user.Email))
			}
			// Add validation token indexes for all pending validations
			for _, pv := range user.PendingValidation {
				if pv != nil && pv.ValidationToken != "" {
					wrapper.Indexes(common.UpdateIndex(USER_BY_VALIDATION_TOKEN_INDEX, pv.ValidationToken))
				}
			}
			// Add password reset token index if present
			if user.PasswordResetToken != "" {
				wrapper.Indexes(common.UpdateIndex(USER_BY_PASSWORD_RESET_TOKEN_INDEX, user.PasswordResetToken))
			}
			err = obj.Rewrap(wrapper)
			if err != nil {
				err = fmt.Errorf("error rewrapping user: %v", err)
				return
			}
		} else {
			exists = true
		}
		return
	}

	providerRef := &AuthProviderRef{}
	if provider != "" {
		providerRef.ProviderName = provider
	}
	err = uconfig.Storage.MutateObjectFromIndex(uconfig.UsersIndexBaseKey, ProviderIdentityString(identity, providerRef),
		USER_BY_IDENTITY_INDEX, mutator, common.OptionDebuglevel(uconfig.DebugLevel), common.OptionOrCreate())
	if err != nil {
		err = fmt.Errorf("Error mutating object: %v", err)
	}
	return exists, err
}

func ProviderIdentityString(identity string, providerRef *AuthProviderRef) string {
	provider := ""
	if providerRef != nil {
		provider = providerRef.ProviderName
	}
	if provider == "" {
		provider = "izcr"  // default provider
	}
	return fmt.Sprintf("%s:%s", provider, identity)
}

func ListUsers(filter string) (users []*User, err error) {
	if filter == "" {
		// Implementation of the empty filter case
		userMap, err := uconfig.Storage.GetObjectsByIndex(uconfig.UsersIndexBaseKey, USER_BY_IDENTITY_INDEX, "")
		if err != nil {
			return nil, fmt.Errorf("error listing users: %v", err)
		}
		users = make([]*User, 0)
		for _, userObj := range userMap {
			if user, ok := userObj.(*User); ok {
				users = append(users, user)
			}
		}
		return users, nil
	} else {
		var filterit *filterlib.Filter
		filterit, err = filterlib.NewFilter(filter)
		if err != nil {
			return nil, fmt.Errorf("error creating filter: %v", err)
		}
		userMap, err := uconfig.Storage.GetObjectsByIndex(uconfig.UsersIndexBaseKey, USER_BY_IDENTITY_INDEX, "")
		if err != nil {
			return nil, fmt.Errorf("error listing users: %v", err)
		}
		users = make([]*User, 0)
		for _, userObj := range userMap {
			if user, ok := userObj.(*User); ok {
				if filterit.Match(user.ExternalIdentity) {
					users = append(users, user)
				}
			}
		}
		return users, nil
	}
}

// return nil, nil

func FindUserByIdentity(identity string, provider string) (user *User, err error) {
	providerRef := &AuthProviderRef{}
	if provider != "" {
		providerRef.ProviderName = provider
	}
	objuser, err := uconfig.Storage.GetObjectByCollectionIndex(uconfig.UsersIndexBaseKey, USER_BY_IDENTITY_INDEX, ProviderIdentityString(identity, providerRef))
	if err != nil {
		return nil, fmt.Errorf("error getting user by identity: %v", err)
	}
	if objuser == nil {
		return nil, fmt.Errorf("user not found")
	}
	user, ok := objuser.(*User)
	if !ok {
		return nil, fmt.Errorf("error casting user")
	}
	return
}

func FindUserByEmail(email string) (user *User, err error) {
	objuser, err := uconfig.Storage.GetObjectByCollectionIndex(uconfig.UsersIndexBaseKey, USER_BY_EMAIL_INDEX, email)
	if err != nil {
		return nil, fmt.Errorf("error getting user by email: %v", err)
	}
	if objuser == nil {
		return nil, fmt.Errorf("user not found")
	}
	user, ok := objuser.(*User)
	if !ok {
		return nil, fmt.Errorf("error casting user")
	}
	return
}

func DeleteUserByIdentity(identity string, provider string) (err error) {
	// Get all users to find the target user
	userMap, err := uconfig.Storage.GetObjectsByIndex(uconfig.UsersIndexBaseKey, USER_BY_IDENTITY_INDEX, "")
	if err != nil {
		return fmt.Errorf("error listing users: %v", err)
	}

	var targetUser *User
	var targetKey string
	providerRef := &AuthProviderRef{}
	if provider != "" {
		providerRef.ProviderName = provider
	}
	providerIdentity := ProviderIdentityString(identity, providerRef)

	// Find the user with matching identity
	for key, userObj := range userMap {
		if user, ok := userObj.(*User); ok {
			if ProviderIdentityString(user.ExternalIdentity, user.AuthProvider) == providerIdentity {
				targetUser = user
				targetKey = uconfig.Storage.Join(uconfig.UsersIndexBaseKey, key)
				break
			}
		}
	}

	if targetUser == nil {
		return fmt.Errorf("not found")
	}

	// Delete the primary user object
	err = uconfig.Storage.Delete(targetKey)
	if err != nil {
		return fmt.Errorf("error deleting user object: %v", err)
	}

	// Manually clean up identity index
	identityIndexKey := uconfig.Storage.Join(uconfig.UsersIndexBaseKey, "_index_", USER_BY_IDENTITY_INDEX, providerIdentity)
	err = uconfig.Storage.Delete(identityIndexKey)
	if err != nil {
		userLog.Warnf("Failed to delete identity index %s: %v", identityIndexKey, err)
	}

	// Manually clean up email index if user has email
	if targetUser.Email != "" {
		emailIndexKey := uconfig.Storage.Join(uconfig.UsersIndexBaseKey, "_index_", USER_BY_EMAIL_INDEX, targetUser.Email)
		err = uconfig.Storage.Delete(emailIndexKey)
		if err != nil {
			userLog.Warnf("Failed to delete email index %s: %v", emailIndexKey, err)
		}
	}

	return nil
}

func DeleteUserByEmail(email string) (err error) {
	// Get all users to find the target user
	userMap, err := uconfig.Storage.GetObjectsByIndex(uconfig.UsersIndexBaseKey, USER_BY_IDENTITY_INDEX, "")
	if err != nil {
		return fmt.Errorf("error listing users: %v", err)
	}

	var targetUser *User
	var targetKey string

	// Find the user with matching email
	for key, userObj := range userMap {
		if user, ok := userObj.(*User); ok {
			if user.Email == email {
				targetUser = user
				targetKey = uconfig.Storage.Join(uconfig.UsersIndexBaseKey, key)
				break
			}
		}
	}

	if targetUser == nil {
		return fmt.Errorf("not found")
	}

	// Delete the primary user object
	err = uconfig.Storage.Delete(targetKey)
	if err != nil {
		return fmt.Errorf("error deleting user object: %v", err)
	}

	// Manually clean up identity index
	providerIdentity := ProviderIdentityString(targetUser.ExternalIdentity, targetUser.AuthProvider)
	identityIndexKey := uconfig.Storage.Join(uconfig.UsersIndexBaseKey, "_index_", USER_BY_IDENTITY_INDEX, providerIdentity)
	err = uconfig.Storage.Delete(identityIndexKey)
	if err != nil {
		userLog.Warnf("Failed to delete identity index %s: %v", identityIndexKey, err)
	}

	// Manually clean up email index
	emailIndexKey := uconfig.Storage.Join(uconfig.UsersIndexBaseKey, "_index_", USER_BY_EMAIL_INDEX, email)
	err = uconfig.Storage.Delete(emailIndexKey)
	if err != nil {
		userLog.Warnf("Failed to delete email index %s: %v", emailIndexKey, err)
	}

	return nil
}

// FindUserByUuid finds a user by UUID
func FindUserByUuid(uuid string) (user *User, err error) {
	objuser, err := uconfig.Storage.GetObject(uconfig.Storage.Join(uconfig.UsersIndexBaseKey, uuid))
	if err != nil {
		return nil, fmt.Errorf("error getting user by uuid: %v", err)
	}
	if objuser == nil {
		return nil, fmt.Errorf("user not found")
	}
	user, ok := objuser.(*User)
	if !ok {
		return nil, fmt.Errorf("error casting user")
	}
	return
}

// DeleteUserByUuid deletes a user by UUID (direct key lookup - most efficient)
func DeleteUserByUuid(uuid string) error {
	// First find the user to get their identity and email for index cleanup
	user, err := FindUserByUuid(uuid)
	if err != nil {
		return fmt.Errorf("error finding user by uuid: %v", err)
	}
	if user == nil {
		return fmt.Errorf("user not found")
	}

	// Delete the primary user object
	targetKey := uconfig.Storage.Join(uconfig.UsersIndexBaseKey, uuid)
	err = uconfig.Storage.Delete(targetKey)
	if err != nil {
		return fmt.Errorf("error deleting user object: %v", err)
	}

	// Manually clean up identity index
	providerIdentity := ProviderIdentityString(user.ExternalIdentity, user.AuthProvider)
	identityIndexKey := uconfig.Storage.Join(uconfig.UsersIndexBaseKey, "_index_", USER_BY_IDENTITY_INDEX, providerIdentity)
	err = uconfig.Storage.Delete(identityIndexKey)
	if err != nil {
		userLog.Warnf("Failed to delete identity index %s: %v", identityIndexKey, err)
	}

	// Manually clean up email index if user has email
	if user.Email != "" {
		emailIndexKey := uconfig.Storage.Join(uconfig.UsersIndexBaseKey, "_index_", USER_BY_EMAIL_INDEX, user.Email)
		err = uconfig.Storage.Delete(emailIndexKey)
		if err != nil {
			userLog.Warnf("Failed to delete email index %s: %v", emailIndexKey, err)
		}
	}

	return nil
}

// CreateOrFailIfUserExistsByIdentity is a wrapper for backward compatibility with tests
// It calls CreateOrFailIfExistsByIdentity with "internal" as the default provider
func CreateOrFailIfUserExistsByIdentity(identity string, mutatorfunc UserMutator) (exists bool, err error) {
	return CreateOrFailIfExistsByIdentity(identity, "internal", mutatorfunc)
}

// UpdateUserByUuid updates an existing user by UUID using the mutator function
// Returns error if user not found
func UpdateUserByUuid(uuid string, mutatorfunc UserMutator) (err error) {
	mutator := func(key string, obj *common.Unwrappable) (err error, dodel bool) {
		err = obj.Unwrap()
		if err != nil {
			err = fmt.Errorf("error unwrapping user: %v", err)
			return
		}

		if obj.Obj() == nil {
			return fmt.Errorf("user not found"), false
		}

		// Update the user
		user := obj.Obj().(*User)
		user.UpdatedAt = timestamppb.Now()
		err, dodel = mutatorfunc(user, false)
		if err != nil {
			err = fmt.Errorf("error mutating user - mutatorfunc: %v", err)
			return
		}

		if dodel {
			return nil, true
		}

		wrapper := SerWrapUser(user)
		wrapper.SetCollectionBase(uconfig.UsersIndexBaseKey)
		wrapper.SetCollectionKey(user.Uuid)
		wrapper.Indexes(common.UpdateIndex(USER_BY_IDENTITY_INDEX, ProviderIdentityString(user.ExternalIdentity, user.AuthProvider)))
		if user.Email != "" {
			wrapper.Indexes(common.UpdateIndex(USER_BY_EMAIL_INDEX, user.Email))
		}
		// Add validation token indexes for all pending validations
		for _, pv := range user.PendingValidation {
			if pv != nil && pv.ValidationToken != "" {
				wrapper.Indexes(common.UpdateIndex(USER_BY_VALIDATION_TOKEN_INDEX, pv.ValidationToken))
			}
		}
		// Add password reset token index if present
		if user.PasswordResetToken != "" {
			wrapper.Indexes(common.UpdateIndex(USER_BY_PASSWORD_RESET_TOKEN_INDEX, user.PasswordResetToken))
		}
		err = obj.Rewrap(wrapper)
		if err != nil {
			err = fmt.Errorf("error rewrapping user: %v", err)
			return
		}
		return
	}

	key := uconfig.Storage.Join(uconfig.UsersIndexBaseKey, uuid)
	err = uconfig.Storage.MutateObject(key, mutator, common.OptionDebuglevel(uconfig.DebugLevel))
	if err != nil {
		err = fmt.Errorf("Error mutating object: %v", err)
	}
	return err
}

// FindUserByValidationToken finds a user by their email validation token.
// Returns nil, nil if user is not found (not an error condition).
func FindUserByValidationToken(token string) (user *User, err error) {
	if token == "" {
		return nil, fmt.Errorf("validation token is required")
	}
	objuser, err := uconfig.Storage.GetObjectByCollectionIndex(uconfig.UsersIndexBaseKey, USER_BY_VALIDATION_TOKEN_INDEX, token)
	if err != nil {
		return nil, fmt.Errorf("error getting user by validation token: %v", err)
	}
	if objuser == nil {
		return nil, nil // Not found, but not an error
	}
	user, ok := objuser.(*User)
	if !ok {
		return nil, fmt.Errorf("error casting user")
	}
	return
}

// DeleteValidationTokenIndex manually removes a validation token index entry.
// This should be called when a validation token is consumed or expired.
func DeleteValidationTokenIndex(token string) error {
	if token == "" {
		return fmt.Errorf("validation token is required")
	}
	tokenIndexKey := uconfig.Storage.Join(uconfig.UsersIndexBaseKey, "_index_", USER_BY_VALIDATION_TOKEN_INDEX, token)
	err := uconfig.Storage.Delete(tokenIndexKey)
	if err != nil {
		userLog.Warnf("Failed to delete validation token index %s: %v", tokenIndexKey, err)
		return err
	}
	return nil
}

// FindUserByPasswordResetToken finds a user by their password reset token.
// Returns nil, nil if user is not found (not an error condition).
func FindUserByPasswordResetToken(token string) (user *User, err error) {
	if token == "" {
		return nil, fmt.Errorf("password reset token is required")
	}
	objuser, err := uconfig.Storage.GetObjectByCollectionIndex(uconfig.UsersIndexBaseKey, USER_BY_PASSWORD_RESET_TOKEN_INDEX, token)
	if err != nil {
		return nil, fmt.Errorf("error getting user by password reset token: %v", err)
	}
	if objuser == nil {
		return nil, nil // Not found, but not an error
	}
	user, ok := objuser.(*User)
	if !ok {
		return nil, fmt.Errorf("error casting user")
	}
	return
}

// DeletePasswordResetTokenIndex manually removes a password reset token index entry.
// This should be called when a reset token is consumed or expired.
func DeletePasswordResetTokenIndex(token string) error {
	if token == "" {
		return fmt.Errorf("password reset token is required")
	}
	tokenIndexKey := uconfig.Storage.Join(uconfig.UsersIndexBaseKey, "_index_", USER_BY_PASSWORD_RESET_TOKEN_INDEX, token)
	err := uconfig.Storage.Delete(tokenIndexKey)
	if err != nil {
		userLog.Warnf("Failed to delete password reset token index %s: %v", tokenIndexKey, err)
		return err
	}
	return nil
}

// NOTE: These methods are commented out as the underlying Role/Tenant/GoodTokenVersion fields
// no longer exist in the new protobuf schema. The new schema uses SystemRoles and ProjectRoles.
// These methods need to be reimplemented to work with the new structure.

// func (u *User) AddRole(r *Role) {
// 	// TODO: Reimplement using SystemRoles and ProjectRoles
// 	// System roles should be added to u.SystemRoles
// 	// Project roles should be added to u.ProjectRoles
// }

// func (u *User) RemoveRole(r *Role) {
// 	// TODO: Reimplement using SystemRoles and ProjectRoles
// 	// System roles should be removed from u.SystemRoles
// 	// Project roles should be removed from u.ProjectRoles
// }
