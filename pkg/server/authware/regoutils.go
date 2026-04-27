package authware

const (
	RegoUtils = `
package utils
# Function to check if an array contains a specific value
contains(arr, value) {
	some i  # Iterate over the array
	arr[i] == value
	# x == value
	# x := arr[_]
}

contains_any_ignore_case_arr(input_list_arr,value_list){
#     input_list_array := split(lower(input_list),",")
		diff := {b | b:= lower(value_list[_]) } & {a | a := lower(input_list_arr[_])}
		count(diff) > 0
}

contains_all_ignore_case(input_list,value_list){
	input_list_array := split(lower(input_list),",")
	count( {b | b:= lower(value_list[_]) } - {a | a := lower(input_list_array[_])}) == 0
	}

contains_any_ignore_case(input_list,value_list){
		input_list_array := split(lower(input_list),",")
		diff := {b | b:= lower(value_list[_]) } & {a | a := lower(input_list_array[_])}
		count(diff) > 0
	}
`
)
