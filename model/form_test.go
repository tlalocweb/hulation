package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// NOTE: the extra slashes are needed for the regex escaping in JSON
const contactInfoFormSchema = `{
    "type": "object",
    "properties": {
        "firstName": {
            "type": "string"
        },
        "lastName": {
            "type": "string"
        },
		"email": {
			"type": "string"
		},
		"phone": {
			"type": "string",
			"pattern": "^[0-9\\-\\(\\)]{5,}$"
		}
    },
    "required": [ "email" ]
}`

func TestFormBasicValidation(t *testing.T) {
	// Create new form
	formmodel, err := CreateNewFormModel("TestFormBasicValidation", "TestFormBasicValidation", "Test Form", contactInfoFormSchema, TURNSTILE_CAPTCHA, "feedback")
	// Create new form
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
		return
	}
	var id string
	id, err = formmodel.ValidateNewModel(testdb)
	if err != nil {
		t.Logf("Unexpected error on Validate(): %v", err)
		return
	}
	err = formmodel.Commit(testdb)
	if err != nil {
		t.Errorf("Error committing form model: %v", err)
		return
	} else {
		t.Log("Insert ok. ID of new form is: ", id)
	}

	// Create new submission
	v := NewVisitor()

	formdata := `{ "firstName": "John", "lastName": "Doe", "email": "johndoe@mail.com", "phone": "1234567890" }`

	submission, err2 := formmodel.NewFormSubmissionWithEvent(v, formdata)

	if err2 != nil {
		t.Errorf("Error creating form submission: %v", err2)
	} else {
		t.Log("Insert ok. ID of new form submission is: ", submission.Form.ID)
	}

	assert.NotNil(t, submission)

	if submission != nil {

		err = submission.Commit(testdb)
		if err != nil {
			t.Errorf("Error creating form submission: %v", err)
		}
		assert.NotNil(t, submission)

		// delete submission
		err = submission.Form.Delete(testdb)

		if err != nil {
			t.Errorf("Error deleting form submission: %v", err)
		} else {
			t.Log("Delete ok.")
		}

		// Delete form
		err = testdb.Delete(formmodel).Error

		if err != nil {
			t.Errorf("Error deleting form: %v", err)
		} else {
			t.Log("Delete ok.")
		}

	}

}

func TestFormEntryShouldNotValidate(t *testing.T) {
	// Create new form
	formmodel, err := CreateNewFormModel("TestFormShouldNotValidate", "TestFormShouldNotValidate", "Test Form", contactInfoFormSchema, TURNSTILE_CAPTCHA, "feedback")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
		return
	}
	var id string
	id, err = formmodel.ValidateNewModel(testdb)
	if err != nil {
		t.Logf("Unexpected error on Validate(): %v", err)
		return
	}
	// Create new form
	if err != nil {
		err = formmodel.Commit(testdb)
	}
	if err != nil {
		t.Errorf("Error creating form model: %v", err)
	} else {
		t.Log("Insert ok. ID of new form is: ", id)
	}

	// Create new submission
	v := NewVisitor()

	formdata := `{ "firstName": "John", "lastName": "Doe", "email": "johndoe@mail.com", "phone": "1" }`

	submission, err2 := formmodel.NewFormSubmissionWithEvent(v, formdata)

	if err2 != nil {
		t.Logf("Correctly errored. Invalid phone number: %v", err2)
	} else {
		t.Errorf("Should have error: %v", err2)

	}

	assert.Nil(t, submission)

}

func TestFormEntryShouldNotValidate2(t *testing.T) {
	// Create new form
	formmodel := &FormModel{
		Name:        "TestFormShouldNotValidate2",
		Description: "Test Form",
		Schema:      contactInfoFormSchema,
	}
	var err error
	var id string
	id, err = formmodel.ValidateNewModel(testdb)
	if err != nil {
		t.Logf("Unexpected error on Validate(): %v", err)
		return
	}

	// Create new form
	err = formmodel.Commit(testdb)

	if err != nil {
		t.Errorf("Error creating form model: %v", err)
	} else {
		t.Log("Insert ok. ID of new form is: ", id)
	}

	// Create new submission
	v := NewVisitor()

	formdata := `{ "firstName": "John", "lastName": "Doe", "phone": "1234556678" }`

	submission, err2 := formmodel.NewFormSubmissionWithEvent(v, formdata)

	if err2 != nil {
		t.Logf("Correctly errored. No email: %v", err2)
	} else {
		t.Errorf("Should have error: %v", err2)

	}

	assert.Nil(t, submission)

}

func TestFormSchemaCache(t *testing.T) {
	// Create new form
	formmodel, err := CreateNewFormModel("TestSchemaCache", "TestSchemaCache", "Test Form", contactInfoFormSchema, TURNSTILE_CAPTCHA, "feedback")
	// Create new form
	if err != nil {
		err = formmodel.Commit(testdb)
	}

	if err != nil {
		t.Errorf("Error creating form model: %v", err)
	} else {
		t.Log("Insert ok. ID of new form is: ", formmodel.ID)
	}

	// Create new submission
	v := NewVisitor()

	formdata := `{ "firstName": "John", "lastName": "Doe", "email": "johndoe@mail.com", "phone": "1234567890" }`

	submission, err2 := formmodel.NewFormSubmissionWithEvent(v, formdata)

	if err2 != nil {
		t.Errorf("Error creating form submission: %v", err2)
	} else {
		t.Log("Insert ok. ID of new submission is: ", submission.Form.ID)
	}

	assert.NotNil(t, submission)

	if submission == nil {
		return
	}
	err = submission.Commit(testdb)
	if err != nil {
		t.Errorf("Error creating form submission: %v", err)
	}
	assert.NotNil(t, submission)

	// delete submission
	err = submission.Form.Delete(testdb)

	if err != nil {
		t.Errorf("Error deleting form submission: %v", err)
	} else {
		t.Log("Delete ok.")
	}

	// make another submission - should use cache

	submission, err2 = formmodel.NewFormSubmissionWithEvent(v, formdata)

	if err2 != nil {
		t.Errorf("Error creating form submission 2: %v", err2)
	} else {
		t.Log("Insert ok. ID of new submission is: ", submission.Form.ID)
	}

	assert.NotNil(t, submission)

	if submission == nil {
		return
	}
	err = submission.Commit(testdb)
	if err != nil {
		t.Errorf("Error creating form submission: %v", err)
	}
	assert.NotNil(t, submission)

	// delete submission
	err = submission.Form.Delete(testdb)

	if err != nil {
		t.Errorf("Error deleting form submission: %v", err)
	} else {
		t.Log("Delete ok.")
	}

	// Delete form
	err = formmodel.Delete(testdb)

	if err != nil {
		t.Errorf("Error deleting form: %v", err)
	} else {
		t.Log("Delete ok.")
	}

	// check that form is not in cache
	submission, err2 = formmodel.NewFormSubmissionWithEvent(v, formdata)

	if err2 != nil {
		t.Log("Should error. Error is: ", err2.Error())
		assert.Equal(t, "form model is deleted", err2.Error())
	} else {
		t.Errorf("Should have errored with 'form model is deleted'")
	}
	assert.Nil(t, submission)

}
